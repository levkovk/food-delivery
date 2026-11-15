// Сервис заказов: приём заказов, их статусы и история.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"fooddelivery/internal/events"
	"fooddelivery/internal/metrics"
	"fooddelivery/internal/platform"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type service struct {
	*platform.App
	restaurantURL string
	client        *http.Client
	created       *metrics.Counter
	transitions   *metrics.Counter
}

func main() {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	app := platform.Start("order-service", withOutbox(sub), 101, true)
	s := &service{
		App:           app,
		restaurantURL: platform.Env("RESTAURANT_URL", "http://restaurant-service:8080"),
		client:        &http.Client{Timeout: 3 * time.Second},
		created:       app.Server.Metrics.NewCounter("orders_created_total", "Orders accepted", "zone"),
		transitions:   app.Server.Metrics.NewCounter("order_status_transitions_total", "Order status changes", "status"),
	}
	mux := app.Server.Mux
	mux.HandleFunc("POST /orders", s.create)
	mux.HandleFunc("GET /orders", s.list)
	mux.HandleFunc("GET /orders/{id}", s.get)
	mux.HandleFunc("POST /orders/{id}/cancel", s.cancel)
	mux.HandleFunc("GET /stats", s.stats)
	app.Run(
		app.Relay(),
		app.Consumer("order-service", []string{events.TopicPayments, events.TopicKitchen, events.TopicDelivery}, s.onEvent),
	)
}

// withOutbox добавляет к миграциям сервиса общие таблицы буфера событий.
func withOutbox(sub fs.FS) fs.FS {
	return platform.MergeFS(sub, "000_outbox.sql", platform.OutboxSchema)
}

type orderItem struct {
	ItemID int64 `json:"item_id"`
	Qty    int   `json:"qty"`
}

type createRequest struct {
	RestaurantID int64       `json:"restaurant_id"`
	Items        []orderItem `json:"items"`
	Address      string      `json:"address"`
}

type quoteLine struct {
	ItemID int64  `json:"item_id"`
	Name   string `json:"name"`
	Qty    int    `json:"qty"`
	Price  int64  `json:"price"`
}

type quote struct {
	RestaurantName string      `json:"restaurant_name"`
	Zone           string      `json:"zone"`
	PrepSeconds    int         `json:"prep_seconds"`
	Total          int64       `json:"total"`
	Lines          []quoteLine `json:"lines"`
}

func (r createRequest) validate() error {
	if r.RestaurantID <= 0 {
		return errors.New("restaurant_id is required")
	}
	if len(r.Items) == 0 || len(r.Items) > 20 {
		return errors.New("order must contain from 1 to 20 items")
	}
	for _, it := range r.Items {
		if it.ItemID <= 0 || it.Qty < 1 || it.Qty > 50 {
			return errors.New("invalid item or quantity")
		}
	}
	if l := len(strings.TrimSpace(r.Address)); l < 5 || l > 200 {
		return errors.New("address must be 5..200 characters")
	}
	return nil
}

// quote запрашивает у сервиса ресторанов актуальные цены и время готовки:
// клиенту нельзя доверять сумму заказа.
func (s *service) quote(ctx context.Context, req createRequest) (*quote, int, error) {
	body, _ := json.Marshal(map[string]any{"restaurant_id": req.RestaurantID, "items": req.Items})
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.restaurantURL+"/internal/quote", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("restaurant service unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return nil, http.StatusUnprocessableEntity, errors.New(e.Error)
	}
	var q quote
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		return nil, http.StatusBadGateway, err
	}
	return &q, 0, nil
}

func (s *service) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := platform.Decode(w, r, &req); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	q, code, err := s.quote(r.Context(), req)
	if err != nil {
		platform.Error(w, code, err.Error())
		return
	}
	user, _ := platform.User(r)
	items, _ := json.Marshal(q.Lines)
	var id int64
	err = s.Tx(r.Context(), func(tx *sql.Tx) error {
		err := tx.QueryRowContext(r.Context(), `INSERT INTO orders
			(restaurant_id, restaurant_name, items, total, zone, address, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			req.RestaurantID, q.RestaurantName, items, q.Total, q.Zone, strings.TrimSpace(req.Address), user).Scan(&id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_history(order_id, status, event_type) VALUES ($1,$2,$3)`,
			id, StatusCreated, events.OrderCreated); err != nil {
			return err
		}
		return platform.Enqueue(r.Context(), tx, s.Name, events.TopicOrders, events.OrderCreated, id, events.OrderCreatedData{
			RestaurantID: req.RestaurantID, Total: q.Total, Zone: q.Zone, PrepSeconds: q.PrepSeconds, Address: req.Address,
		})
	})
	if err != nil {
		s.Log.Error("create order", "err", err)
		platform.Error(w, http.StatusInternalServerError, "could not create order")
		return
	}
	s.created.Inc(q.Zone)
	s.Log.Info("order created", "order", id, "total", q.Total, "zone", q.Zone, "user", user)
	platform.JSON(w, http.StatusCreated, map[string]any{"id": id, "status": StatusCreated, "total": q.Total, "lines": q.Lines})
}

type order struct {
	ID             int64           `json:"id"`
	RestaurantID   int64           `json:"restaurant_id"`
	RestaurantName string          `json:"restaurant_name"`
	Items          json.RawMessage `json:"items"`
	Total          int64           `json:"total"`
	Zone           string          `json:"zone"`
	Address        string          `json:"address"`
	Status         string          `json:"status"`
	CourierName    *string         `json:"courier_name"`
	CreatedBy      string          `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

const orderColumns = `id, restaurant_id, restaurant_name, items, total, zone, address, status, courier_name, created_by, created_at, updated_at`

func scanOrder(row interface{ Scan(...any) error }) (order, error) {
	var o order
	err := row.Scan(&o.ID, &o.RestaurantID, &o.RestaurantName, &o.Items, &o.Total, &o.Zone, &o.Address,
		&o.Status, &o.CourierName, &o.CreatedBy, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (s *service) list(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	rows, err := s.DB.QueryContext(r.Context(), `SELECT `+orderColumns+` FROM orders
		WHERE ($1 = '' OR status = $1) ORDER BY id DESC LIMIT 200`, status)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			platform.Error(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, o)
	}
	platform.JSON(w, http.StatusOK, out)
}

func (s *service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	if !ok {
		platform.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	o, err := scanOrder(s.DB.QueryRowContext(r.Context(), `SELECT `+orderColumns+` FROM orders WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		platform.Error(w, http.StatusNotFound, "order not found")
		return
	} else if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT status, event_type, coalesce(note,''), at
		FROM order_history WHERE order_id=$1 ORDER BY at, id`, id)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	type entry struct {
		Status string    `json:"status"`
		Event  string    `json:"event"`
		Note   string    `json:"note"`
		At     time.Time `json:"at"`
	}
	history := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Status, &e.Event, &e.Note, &e.At); err == nil {
			history = append(history, e)
		}
	}
	platform.JSON(w, http.StatusOK, map[string]any{"order": o, "history": history})
}

func (s *service) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	if !ok {
		platform.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	user, _ := platform.User(r)
	var status string
	err := s.Tx(r.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(r.Context(), `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
			return err
		}
		if !Cancellable(status) {
			return errConflict
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE orders SET status=$2, updated_at=now() WHERE id=$1`, id, StatusCancelled); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_history(order_id, status, event_type, note) VALUES ($1,$2,$3,$4)`,
			id, StatusCancelled, events.OrderCancelled, "cancelled by "+user); err != nil {
			return err
		}
		return platform.Enqueue(r.Context(), tx, s.Name, events.TopicOrders, events.OrderCancelled, id, map[string]string{"by": user})
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		platform.Error(w, http.StatusNotFound, "order not found")
	case errors.Is(err, errConflict):
		platform.Error(w, http.StatusConflict, "order in status "+status+" cannot be cancelled")
	case err != nil:
		platform.Error(w, http.StatusInternalServerError, "cancel failed")
	default:
		s.transitions.Inc(StatusCancelled)
		platform.JSON(w, http.StatusOK, map[string]any{"id": id, "status": StatusCancelled})
	}
}

var errConflict = errors.New("conflict")

func (s *service) stats(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT status, count(*) FROM orders GROUP BY status`)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	byStatus := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if rows.Scan(&st, &n) == nil {
			byStatus[st] = n
		}
	}
	var avg sql.NullFloat64
	s.DB.QueryRowContext(r.Context(), `SELECT avg(extract(epoch FROM h.at - o.created_at)) FROM orders o
		JOIN order_history h ON h.order_id = o.id AND h.status = 'delivered'`).Scan(&avg) //nolint:errcheck
	var revenue sql.NullInt64
	s.DB.QueryRowContext(r.Context(), `SELECT sum(total) FROM orders WHERE status = 'delivered'`).Scan(&revenue) //nolint:errcheck
	platform.JSON(w, http.StatusOK, map[string]any{
		"by_status":            byStatus,
		"avg_delivery_seconds": avg.Float64,
		"revenue":              revenue.Int64,
	})
}

// targetStatus сопоставляет событию статус заказа и примечание для истории.
func targetStatus(ev events.Event) (string, string) {
	switch ev.Type {
	case events.PaymentAuthorized:
		return StatusPaid, ""
	case events.PaymentDeclined:
		var d events.PaymentData
		ev.Decode(&d) //nolint:errcheck
		return StatusDeclined, d.Reason
	case events.KitchenAccepted:
		return StatusCooking, ""
	case events.KitchenReady:
		return StatusReady, ""
	case events.CourierAssigned:
		var d events.DeliveryData
		ev.Decode(&d) //nolint:errcheck
		return StatusDelivering, d.CourierName
	case events.DeliveryNoCouriers:
		return "", "no free couriers in zone, waiting"
	case events.DeliveryCompleted:
		return StatusDelivered, ""
	}
	return "", ""
}

func (s *service) onEvent(ctx context.Context, ev events.Event) error {
	target, note := targetStatus(ev)
	if target == "" && note == "" {
		return nil
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		first, err := platform.FirstTime(ctx, tx, ev.ID)
		if err != nil || !first {
			return err
		}
		var current string
		err = tx.QueryRowContext(ctx, `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, ev.OrderID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			s.Log.Warn("event for unknown order", "order", ev.OrderID, "type", ev.Type)
			return nil
		} else if err != nil {
			return err
		}
		next, changed := Next(current, target)
		if changed {
			courier := sql.NullString{}
			if target == StatusDelivering {
				courier = sql.NullString{String: note, Valid: note != ""}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE orders SET status=$2, courier_name=coalesce($3, courier_name),
				updated_at=now() WHERE id=$1`, ev.OrderID, next, courier); err != nil {
				return err
			}
			s.transitions.Inc(next)
		}
		if changed || target == "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO order_history(order_id, status, event_type, note, at) VALUES ($1,$2,$3,$4,$5)`,
				ev.OrderID, next, ev.Type, sql.NullString{String: note, Valid: note != ""}, ev.At)
		}
		return err
	})
}
