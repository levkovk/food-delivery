// API-шлюз: вход пользователей, проверка JWT и прав, ограничение частоты
// запросов и проксирование во внутренние сервисы.
package main

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"fooddelivery/internal/auth"
	"fooddelivery/internal/metrics"
	"fooddelivery/internal/platform"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type gateway struct {
	*platform.App
	issuer    *auth.Issuer
	proxies   map[string]*httputil.ReverseProxy
	limiter   *limiter
	denied    *metrics.Counter
	limited   *metrics.Counter
	logins    *metrics.Counter
	upstreams *metrics.Counter
}

func main() {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	app := platform.Start("gateway", sub, 100, false)
	issuer, err := auth.NewIssuer(platform.MustEnv(app.Log, "JWT_SECRET"), 8*time.Hour)
	if err != nil {
		app.Log.Error("jwt", "err", err)
		return
	}
	m := app.Server.Metrics
	g := &gateway{
		App:       app,
		issuer:    issuer,
		proxies:   map[string]*httputil.ReverseProxy{},
		limiter:   newLimiter(float64(platform.EnvInt("RATE_LIMIT_RPS", 20)), float64(platform.EnvInt("RATE_LIMIT_BURST", 40))),
		denied:    m.NewCounter("gateway_denied_total", "Rejected requests", "reason"),
		limited:   m.NewCounter("gateway_rate_limited_total", "Requests rejected by rate limiter"),
		logins:    m.NewCounter("gateway_logins_total", "Login attempts", "result"),
		upstreams: m.NewCounter("gateway_upstream_requests_total", "Requests proxied", "upstream", "code"),
	}
	for name, def := range map[string]string{
		"order":        "http://order-service:8080",
		"restaurant":   "http://restaurant-service:8080",
		"courier":      "http://courier-service:8080",
		"payment":      "http://payment-service:8080",
		"notification": "http://notification-service:8080",
	} {
		target, err := url.Parse(platform.Env(strings.ToUpper(name)+"_URL", def))
		if err != nil {
			app.Log.Error("bad upstream url", "upstream", name, "err", err)
			return
		}
		g.proxies[name] = g.newProxy(name, target)
	}
	if err := g.seedUsers(context.Background()); err != nil {
		app.Log.Error("seed users", "err", err)
		return
	}
	app.Server.Mux.HandleFunc("POST /api/auth/login", g.login)
	app.Server.Mux.HandleFunc("GET /api/auth/me", g.me)
	app.Server.Mux.HandleFunc("/api/", g.route)
	app.Run(app.Every(time.Minute, "limiter-cleanup", func(context.Context) error {
		g.limiter.cleanup(time.Now(), 10*time.Minute)
		return nil
	}))
}

func (g *gateway) newProxy(name string, target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = upstreamPath(pr.In.URL.Path)
			pr.Out.URL.RawPath = ""
			pr.SetXForwarded()
		},
		// Отрицательный интервал - немедленная отправка, нужна для SSE.
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			g.upstreams.Inc(name, resp.Status[:3])
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.upstreams.Inc(name, "502")
			g.Log.Warn("upstream error", "upstream", name, "path", r.URL.Path, "err", err)
			platform.Error(w, http.StatusBadGateway, name+" service unavailable")
		},
	}
}

// seedUsers создаёт учётные записи администратора и оператора, если их нет.
// Пароли приходят из секретов Kubernetes и в образ не попадают.
func (g *gateway) seedUsers(ctx context.Context) error {
	for _, u := range []struct{ name, role, env string }{
		{"admin", auth.RoleAdmin, "ADMIN_PASSWORD"},
		{"operator", auth.RoleOperator, "OPERATOR_PASSWORD"},
	} {
		password := platform.MustEnv(g.Log, u.env)
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		if _, err := g.DB.ExecContext(ctx, `INSERT INTO users(username, role, password_hash) VALUES ($1,$2,$3)
			ON CONFLICT (username) DO NOTHING`, u.name, u.role, hash); err != nil {
			return err
		}
	}
	return nil
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-Ip"); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (g *gateway) login(w http.ResponseWriter, r *http.Request) {
	if !g.limiter.allow("login:"+clientIP(r), time.Now()) {
		g.limited.Inc()
		platform.Error(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := platform.Decode(w, r, &req); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	var role, hash string
	err := g.DB.QueryRowContext(r.Context(), `SELECT role, password_hash FROM users WHERE username=$1`, req.Username).Scan(&role, &hash)
	ok := err == nil && auth.CheckPassword(req.Password, hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		platform.Error(w, http.StatusInternalServerError, "login failed")
		return
	}
	g.DB.ExecContext(r.Context(), `INSERT INTO login_audit(username, success, client) VALUES ($1,$2,$3)`, //nolint:errcheck
		req.Username, ok, clientIP(r))
	if !ok {
		g.logins.Inc("failure")
		platform.Error(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := g.issuer.Issue(req.Username, role, time.Now())
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "token error")
		return
	}
	g.logins.Inc("success")
	platform.JSON(w, http.StatusOK, map[string]string{"token": token, "role": role, "username": req.Username})
}

// bearer извлекает токен из заголовка Authorization или, для потока
// событий, из параметра access_token: браузерный EventSource не умеет
// передавать заголовки.
func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if r.URL.Path == "/api/events/stream" {
		return r.URL.Query().Get("access_token")
	}
	return ""
}

func (g *gateway) me(w http.ResponseWriter, r *http.Request) {
	claims, err := g.issuer.Verify(bearer(r))
	if err != nil {
		platform.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	platform.JSON(w, http.StatusOK, map[string]any{"username": claims.Subject, "role": claims.Role, "expires_at": claims.ExpiresAt.Time})
}

func (g *gateway) route(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !g.limiter.allow(ip, time.Now()) {
		g.limited.Inc()
		w.Header().Set("Retry-After", "1")
		platform.Error(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	claims, err := g.issuer.Verify(bearer(r))
	if err != nil {
		g.denied.Inc("unauthenticated")
		platform.Error(w, http.StatusUnauthorized, "authentication required")
		return
	}
	upstream, dec := authorize(r.Method, r.URL.Path, claims.Role)
	switch dec {
	case unknownRoute:
		g.denied.Inc("not_found")
		platform.Error(w, http.StatusNotFound, "route not found")
		return
	case forbidden:
		g.denied.Inc("forbidden")
		platform.Error(w, http.StatusForbidden, "insufficient role")
		return
	}
	// Заголовки личности выставляет только шлюз: значения от клиента затираются.
	r.Header.Set("X-User", claims.Subject)
	r.Header.Set("X-Role", claims.Role)
	r.Header.Del("Authorization")
	q := r.URL.Query()
	q.Del("access_token")
	r.URL.RawQuery = q.Encode()
	g.proxies[upstream].ServeHTTP(w, r)
}
