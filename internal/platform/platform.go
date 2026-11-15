// Package platform содержит общий каркас сервисов: конфигурацию, журнал,
// подключение к PostgreSQL, миграции и HTTP-сервер с проверками и метриками.
package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq" // драйвер PostgreSQL

	"fooddelivery/internal/metrics"
)

// Env возвращает переменную окружения или значение по умолчанию.
func Env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// EnvInt возвращает целочисленную переменную окружения.
func EnvInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

// MustEnv возвращает обязательную переменную или завершает процесс.
func MustEnv(log *slog.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Error("required environment variable is not set", "key", key)
		os.Exit(2)
	}
	return v
}

// Logger создаёт журнал в формате JSON с именем сервиса.
func Logger(service string) *slog.Logger {
	level := slog.LevelInfo
	if Env("LOG_LEVEL", "info") == "debug" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", service)
}

// OpenDB подключается к PostgreSQL, повторяя попытки, пока база не станет
// доступна: в кластере порядок запуска подов не гарантирован.
func OpenDB(ctx context.Context, log *slog.Logger) (*sql.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable search_path=%s",
		Env("DB_HOST", "postgres"), Env("DB_PORT", "5432"), Env("DB_NAME", "food"),
		MustEnv(log, "DB_USER"), MustEnv(log, "DB_PASSWORD"), Env("DB_SCHEMA", "public"))
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(EnvInt("DB_MAX_CONNS", 10))
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		if err == nil {
			return db, nil
		}
		if attempt >= 40 || ctx.Err() != nil {
			return nil, fmt.Errorf("postgres unavailable: %w", err)
		}
		log.Info("waiting for postgres", "attempt", attempt, "err", err)
		time.Sleep(time.Duration(min(attempt, 5)) * time.Second)
	}
}

// Migrate применяет SQL-файлы из fsys по порядку имён. Применённые версии
// хранятся в таблице schema_migrations, а рекомендательная блокировка не даёт
// двум репликам выполнять миграции одновременно.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS, lockKey int64) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey) //nolint:errcheck
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, name); err != nil {
			tx.Rollback() //nolint:errcheck
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Server - HTTP-сервер сервиса с метриками и проверками состояния.
type Server struct {
	Name     string
	Log      *slog.Logger
	Mux      *http.ServeMux
	Metrics  *metrics.Registry
	requests *metrics.Counter
	duration *metrics.Histogram
	mu       sync.Mutex
	checks   map[string]func(context.Context) error
}

// NewServer создаёт сервер с маршрутами /healthz, /readyz и /metrics.
func NewServer(name string, log *slog.Logger) *Server {
	reg := metrics.NewRegistry()
	s := &Server{
		Name:     name,
		Log:      log,
		Mux:      http.NewServeMux(),
		Metrics:  reg,
		requests: reg.NewCounter("http_requests_total", "HTTP requests processed", "route", "code"),
		duration: reg.NewHistogram("http_request_duration_seconds", "HTTP request latency", metrics.DefaultBuckets, "route"),
		checks:   map[string]func(context.Context) error{},
	}
	s.Mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok", "service": name})
	})
	s.Mux.HandleFunc("GET /readyz", s.ready)
	s.Mux.Handle("GET /metrics", reg.Handler())
	return s
}

// AddCheck регистрирует проверку готовности (база данных, брокер и т. п.).
func (s *Server) AddCheck(name string, fn func(context.Context) error) {
	s.mu.Lock()
	s.checks[name] = fn
	s.mu.Unlock()
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]string{}
	code := http.StatusOK
	for name, fn := range s.checks {
		if err := fn(ctx); err != nil {
			result[name] = err.Error()
			code = http.StatusServiceUnavailable
		} else {
			result[name] = "ok"
		}
	}
	JSON(w, code, result)
}

type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush пробрасывает сброс буфера, необходимый для потоковых ответов (SSE).
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Handler оборачивает маршрутизатор учётом метрик и перехватом паник.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				s.Log.Error("panic", "path", r.URL.Path, "panic", p)
				JSON(rec, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			}
			_, pattern := s.Mux.Handler(r)
			if pattern == "" {
				pattern = "unmatched"
			}
			s.requests.Inc(pattern, strconv.Itoa(rec.code))
			s.duration.Observe(time.Since(start).Seconds(), pattern)
		}()
		s.Mux.ServeHTTP(rec, r)
	})
}

// Run запускает сервер и фоновые задачи, а по сигналу SIGTERM завершает их:
// Kubernetes отправляет этот сигнал при остановке пода.
func (s *Server) Run(ctx context.Context, background ...func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{
		Addr:              ":" + Env("PORT", "8080"),
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errc := make(chan error, len(background)+1)
	for _, job := range background {
		go func(job func(context.Context) error) {
			if err := job(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errc <- err
			}
		}(job)
	}
	go func() {
		s.Log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errc:
		s.Log.Error("fatal", "err", err)
		stop()
		shutdown(srv)
		return err
	}
	s.Log.Info("shutting down")
	shutdown(srv)
	return nil
}

func shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx) //nolint:errcheck
}

// JSON записывает ответ в формате JSON.
func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// Error записывает ошибку в формате {"error": "..."}.
func Error(w http.ResponseWriter, code int, msg string) {
	JSON(w, code, map[string]string{"error": msg})
}

// Decode читает тело запроса JSON с ограничением размера.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

// PathID разбирает числовой параметр пути.
func PathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// User возвращает имя и роль, которые шлюз передал в заголовках.
func User(r *http.Request) (string, string) {
	return r.Header.Get("X-User"), r.Header.Get("X-Role")
}

// Hostname возвращает имя пода (в Kubernetes совпадает с именем хоста).
func Hostname() string {
	h, _ := os.Hostname()
	return strings.TrimSpace(h)
}
