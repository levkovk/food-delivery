// Сервис ресторанов: каталог и меню, расчёт стоимости и кухня.
package main

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"fooddelivery/internal/auth"
	"fooddelivery/internal/events"
	"fooddelivery/internal/metrics"
	"fooddelivery/internal/platform"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type service struct {
	*platform.App
	secondsPerMinute float64
	cooked           *metrics.Counter
	cooking          *metrics.Gauge
}

func main() {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	app := platform.Start("restaurant-service", platform.MergeFS(sub, "000_outbox.sql", platform.OutboxSchema), 102, true)
	s := &service{
		App: app,
		// TIME_SCALE - сколько секунд реального времени длится минута модели.
		secondsPerMinute: float64(platform.EnvInt("TIME_SCALE", 1)),
		cooked:           app.Server.Metrics.NewCounter("kitchen_tickets_ready_total", "Orders cooked", "zone"),
		cooking:          app.Server.Metrics.NewGauge("kitchen_tickets_cooking", "Orders being cooked now"),
	}
	mux := app.Server.Mux
	mux.HandleFunc("GET /restaurants", s.listRestaurants)
	mux.HandleFunc("POST /restaurants", s.adminOnly(s.createRestaurant))
	mux.HandleFunc("PUT /restaurants/{id}", s.adminOnly(s.updateRestaurant))
	mux.HandleFunc("DELETE /restaurants/{id}", s.adminOnly(s.deleteRestaurant))
	mux.HandleFunc("GET /restaurants/{id}/menu", s.listMenu)
	mux.HandleFunc("POST /restaurants/{id}/menu", s.adminOnly(s.createItem))
	mux.HandleFunc("PUT /menu/{id}", s.adminOnly(s.updateItem))
	mux.HandleFunc("DELETE /menu/{id}", s.adminOnly(s.deleteItem))
	mux.HandleFunc("POST /internal/quote", s.quote)
	app.Run(
		app.Relay(),
		app.Consumer("restaurant-service", []string{events.TopicPayments, events.TopicOrders}, s.onEvent),
		app.Every(time.Second, "kitchen", s.finishCooking),
	)
}

// adminOnly - повторная проверка роли внутри сервиса на случай, если запрос
// пришёл в обход шлюза (принцип эшелонированной защиты).
func (s *service) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, role := platform.User(r); role != auth.RoleAdmin {
			platform.Error(w, http.StatusForbidden, "admin role required")
			return
		}
		h(w, r)
	}
}

type restaurant struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Cuisine     string `json:"cuisine"`
	Zone        string `json:"zone"`
	PrepMinutes int    `json:"prep_minutes"`
	Active      bool   `json:"active"`
}

var zones = map[string]bool{"center": true, "north": true, "south": true}

func (r restaurant) validate() error {
	if l := len([]rune(strings.TrimSpace(r.Name))); l < 2 || l > 80 {
		return errors.New("name must be 2..80 characters")
	}
	if !zones[r.Zone] {
		return errors.New("zone must be one of center, north, south")
	}
	if r.PrepMinutes < 1 || r.PrepMinutes > 120 {
		return errors.New("prep_minutes must be 1..120")
	}
	return nil
}

func (s *service) listRestaurants(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, name, cuisine, zone, prep_minutes, active FROM restaurants ORDER BY id`)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []restaurant{}
	for rows.Next() {
		var x restaurant
		if rows.Scan(&x.ID, &x.Name, &x.Cuisine, &x.Zone, &x.PrepMinutes, &x.Active) == nil {
			out = append(out, x)
		}
	}
	platform.JSON(w, http.StatusOK, out)
}

func (s *service) createRestaurant(w http.ResponseWriter, r *http.Request) {
	x := restaurant{Active: true}
	if err := platform.Decode(w, r, &x); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := x.validate(); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	err := s.DB.QueryRowContext(r.Context(), `INSERT INTO restaurants(name, cuisine, zone, prep_minutes, active)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, strings.TrimSpace(x.Name), x.Cuisine, x.Zone, x.PrepMinutes, x.Active).Scan(&x.ID)
	if writeErr(w, err) {
		return
	}
	platform.JSON(w, http.StatusCreated, x)
}

func (s *service) updateRestaurant(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	var x restaurant
	if !ok || platform.Decode(w, r, &x) != nil {
		platform.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := x.validate(); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE restaurants SET name=$2, cuisine=$3, zone=$4, prep_minutes=$5, active=$6
		WHERE id=$1`, id, strings.TrimSpace(x.Name), x.Cuisine, x.Zone, x.PrepMinutes, x.Active)
	if writeErr(w, err) || notFound(w, res) {
		return
	}
	x.ID = id
	platform.JSON(w, http.StatusOK, x)
}

func (s *service) deleteRestaurant(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	if !ok {
		platform.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `DELETE FROM restaurants WHERE id=$1`, id)
	if writeErr(w, err) || notFound(w, res) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type item struct {
	ID           int64  `json:"id"`
	RestaurantID int64  `json:"restaurant_id"`
	Name         string `json:"name"`
	Price        int64  `json:"price"`
	Available    bool   `json:"available"`
}

func (i item) validate() error {
	if l := len([]rune(strings.TrimSpace(i.Name))); l < 2 || l > 80 {
		return errors.New("name must be 2..80 characters")
	}
	if i.Price <= 0 || i.Price > 100000 {
		return errors.New("price must be 1..100000")
	}
	return nil
}

func (s *service) listMenu(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	if !ok {
		platform.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, restaurant_id, name, price, available FROM menu_items
		WHERE restaurant_id=$1 ORDER BY id`, id)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []item{}
	for rows.Next() {
		var x item
		if rows.Scan(&x.ID, &x.RestaurantID, &x.Name, &x.Price, &x.Available) == nil {
			out = append(out, x)
		}
	}
	platform.JSON(w, http.StatusOK, out)
}

func (s *service) createItem(w http.ResponseWriter, r *http.Request) {
	rid, ok := platform.PathID(r, "id")
	x := item{Available: true}
	if !ok || platform.Decode(w, r, &x) != nil {
		platform.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := x.validate(); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	x.RestaurantID = rid
	err := s.DB.QueryRowContext(r.Context(), `INSERT INTO menu_items(restaurant_id, name, price, available)
		VALUES ($1,$2,$3,$4) RETURNING id`, rid, strings.TrimSpace(x.Name), x.Price, x.Available).Scan(&x.ID)
	if writeErr(w, err) {
		return
	}
	platform.JSON(w, http.StatusCreated, x)
}

func (s *service) updateItem(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	var x item
	if !ok || platform.Decode(w, r, &x) != nil {
		platform.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := x.validate(); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	err := s.DB.QueryRowContext(r.Context(), `UPDATE menu_items SET name=$2, price=$3, available=$4 WHERE id=$1
		RETURNING restaurant_id`, id, strings.TrimSpace(x.Name), x.Price, x.Available).Scan(&x.RestaurantID)
	if errors.Is(err, sql.ErrNoRows) {
		platform.Error(w, http.StatusNotFound, "not found")
		return
	}
	if writeErr(w, err) {
		return
	}
	x.ID = id
	platform.JSON(w, http.StatusOK, x)
}

func (s *service) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	if !ok {
		platform.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `DELETE FROM menu_items WHERE id=$1`, id)
	if writeErr(w, err) || notFound(w, res) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// quote - внутренний метод для сервиса заказов, через шлюз недоступен.
func (s *service) quote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RestaurantID int64       `json:"restaurant_id"`
		Items        []quoteItem `json:"items"`
	}
	if err := platform.Decode(w, r, &req); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	var rest restaurant
	err := s.DB.QueryRowContext(r.Context(), `SELECT name, zone, prep_minutes, active FROM restaurants WHERE id=$1`,
		req.RestaurantID).Scan(&rest.Name, &rest.Zone, &rest.PrepMinutes, &rest.Active)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !rest.Active) {
		platform.Error(w, http.StatusUnprocessableEntity, "restaurant is not accepting orders")
		return
	} else if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, name, price, available FROM menu_items WHERE restaurant_id=$1`, req.RestaurantID)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	menu := map[int64]menuItem{}
	for rows.Next() {
		var m menuItem
		if rows.Scan(&m.ID, &m.Name, &m.Price, &m.Available) == nil {
			menu[m.ID] = m
		}
	}
	rows.Close()
	lines, total, err := calcQuote(menu, req.Items)
	if err != nil {
		platform.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	platform.JSON(w, http.StatusOK, map[string]any{
		"restaurant_name": rest.Name,
		"zone":            rest.Zone,
		"prep_seconds":    int(float64(rest.PrepMinutes) * s.secondsPerMinute),
		"total":           total,
		"lines":           lines,
	})
}

// onEvent: после оплаты кухня принимает заказ, после отмены - снимает его.
func (s *service) onEvent(ctx context.Context, ev events.Event) error {
	switch ev.Type {
	case events.PaymentAuthorized, events.OrderCancelled:
	default:
		return nil
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		first, err := platform.FirstTime(ctx, tx, ev.ID)
		if err != nil || !first {
			return err
		}
		if ev.Type == events.OrderCancelled {
			_, err := tx.ExecContext(ctx, `DELETE FROM tickets WHERE order_id=$1 AND status='cooking'`, ev.OrderID)
			return err
		}
		var d events.PaymentData
		if err := ev.Decode(&d); err != nil {
			return err
		}
		readyAt := time.Now().Add(time.Duration(d.PrepSeconds) * time.Second)
		res, err := tx.ExecContext(ctx, `INSERT INTO tickets(order_id, restaurant_id, zone, ready_at)
			VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, ev.OrderID, d.RestaurantID, d.Zone, readyAt)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return platform.Enqueue(ctx, tx, s.Name, events.TopicKitchen, events.KitchenAccepted, ev.OrderID,
			events.KitchenData{RestaurantID: d.RestaurantID, Zone: d.Zone, PrepSeconds: d.PrepSeconds})
	})
}

// finishCooking отмечает готовые заказы. Несколько реплик работают
// одновременно и не мешают друг другу благодаря SKIP LOCKED.
func (s *service) finishCooking(ctx context.Context) error {
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT order_id, restaurant_id, zone FROM tickets
			WHERE status='cooking' AND ready_at <= now() ORDER BY ready_at LIMIT 50 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
		}
		type ticket struct {
			order, restaurant int64
			zone              string
		}
		var due []ticket
		for rows.Next() {
			var t ticket
			if err := rows.Scan(&t.order, &t.restaurant, &t.zone); err != nil {
				rows.Close()
				return err
			}
			due = append(due, t)
		}
		rows.Close()
		for _, t := range due {
			if _, err := tx.ExecContext(ctx, `UPDATE tickets SET status='ready' WHERE order_id=$1`, t.order); err != nil {
				return err
			}
			if err := platform.Enqueue(ctx, tx, s.Name, events.TopicKitchen, events.KitchenReady, t.order,
				events.KitchenData{RestaurantID: t.restaurant, Zone: t.zone}); err != nil {
				return err
			}
			s.cooked.Inc(t.zone)
		}
		return nil
	})
	var n int
	if s.DB.QueryRowContext(ctx, `SELECT count(*) FROM tickets WHERE status='cooking'`).Scan(&n) == nil {
		s.cooking.Set(float64(n))
	}
	return err
}

func writeErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "duplicate key") {
		platform.Error(w, http.StatusConflict, "already exists")
	} else if strings.Contains(err.Error(), "foreign key") {
		platform.Error(w, http.StatusNotFound, "restaurant not found")
	} else {
		platform.Error(w, http.StatusInternalServerError, "write failed")
	}
	return true
}

func notFound(w http.ResponseWriter, res sql.Result) bool {
	if n, _ := res.RowsAffected(); n == 0 {
		platform.Error(w, http.StatusNotFound, "not found")
		return true
	}
	return false
}
