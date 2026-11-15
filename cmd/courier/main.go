// Сервис курьеров: смены, назначение заказов и модель доставки.
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

	"github.com/lib/pq"

	"fooddelivery/internal/auth"
	"fooddelivery/internal/events"
	"fooddelivery/internal/metrics"
	"fooddelivery/internal/platform"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type service struct {
	*platform.App
	minute    time.Duration
	assigned  *metrics.Counter
	completed *metrics.Counter
	waiting   *metrics.Gauge
	free      *metrics.Gauge
}

func main() {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	app := platform.Start("courier-service", platform.MergeFS(sub, "000_outbox.sql", platform.OutboxSchema), 104, true)
	m := app.Server.Metrics
	s := &service{
		App:       app,
		minute:    time.Duration(platform.EnvInt("TIME_SCALE", 1)) * time.Second,
		assigned:  m.NewCounter("courier_assignments_total", "Orders assigned to couriers", "zone"),
		completed: m.NewCounter("deliveries_completed_total", "Delivered orders", "zone"),
		waiting:   m.NewGauge("deliveries_waiting", "Ready orders without a courier"),
		free:      m.NewGauge("couriers_free", "Couriers on shift and not busy", "zone"),
	}
	mux := app.Server.Mux
	mux.HandleFunc("GET /couriers", s.list)
	mux.HandleFunc("POST /couriers", s.create)
	mux.HandleFunc("PATCH /couriers/{id}", s.setShift)
	mux.HandleFunc("GET /deliveries", s.deliveries)
	app.Run(
		app.Relay(),
		app.Consumer("courier-service", []string{events.TopicKitchen, events.TopicOrders}, s.onEvent),
		app.Every(time.Second, "dispatch", s.dispatch),
		app.Every(time.Second, "complete", s.complete),
		app.Every(5*time.Second, "gauges", s.gauges),
	)
}

func (s *service) onEvent(ctx context.Context, ev events.Event) error {
	switch ev.Type {
	case events.KitchenReady, events.OrderCancelled:
	default:
		return nil
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		first, err := platform.FirstTime(ctx, tx, ev.ID)
		if err != nil || !first {
			return err
		}
		if ev.Type == events.OrderCancelled {
			_, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE order_id=$1 AND status='waiting'`, ev.OrderID)
			return err
		}
		var d events.KitchenData
		if err := ev.Decode(&d); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO deliveries(order_id, zone) VALUES ($1,$2) ON CONFLICT DO NOTHING`, ev.OrderID, d.Zone)
		return err
	})
}

// dispatch назначает ожидающие заказы свободным курьерам. Заказ и курьер
// блокируются с SKIP LOCKED, поэтому реплики сервиса не назначат одного
// курьера на два заказа и не возьмут один заказ дважды.
func (s *service) dispatch(ctx context.Context) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT order_id, zone, attempts FROM deliveries
			WHERE status='waiting' ORDER BY created_at LIMIT 20 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
		}
		type job struct {
			order    int64
			zone     string
			attempts int
		}
		var jobs []job
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.order, &j.zone, &j.attempts); err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, j)
		}
		rows.Close()
		for _, j := range jobs {
			var id int64
			var name, zone, transport string
			err := tx.QueryRowContext(ctx, `SELECT id, name, zone, transport FROM couriers
				WHERE on_shift AND NOT busy AND zone = ANY($1)
				ORDER BY (zone = $2) DESC, deliveries, id LIMIT 1 FOR UPDATE SKIP LOCKED`,
				pq.Array(candidateZones(j.zone, j.attempts)), j.zone).Scan(&id, &name, &zone, &transport)
			if errors.Is(err, sql.ErrNoRows) {
				if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET attempts = attempts + 1 WHERE order_id=$1`, j.order); err != nil {
					return err
				}
				if j.attempts == 0 {
					if err := platform.Enqueue(ctx, tx, s.Name, events.TopicDelivery, events.DeliveryNoCouriers, j.order,
						events.DeliveryData{Zone: j.zone, Attempt: 1}); err != nil {
						return err
					}
				}
				continue
			} else if err != nil {
				return err
			}
			eta := time.Now().Add(travelTime(transport, zone == j.zone, s.minute))
			if _, err := tx.ExecContext(ctx, `UPDATE couriers SET busy=true WHERE id=$1`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='assigned', courier_id=$2, assigned_at=now(), eta_at=$3
				WHERE order_id=$1`, j.order, id, eta); err != nil {
				return err
			}
			if err := platform.Enqueue(ctx, tx, s.Name, events.TopicDelivery, events.CourierAssigned, j.order,
				events.DeliveryData{CourierID: id, CourierName: name, Zone: zone, Attempt: j.attempts + 1}); err != nil {
				return err
			}
			s.assigned.Inc(zone)
			s.Log.Info("courier assigned", "order", j.order, "courier", name, "zone", zone)
		}
		return nil
	})
}

// complete завершает доставки, у которых истекло расчётное время в пути.
func (s *service) complete(ctx context.Context) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT d.order_id, d.courier_id, c.name, c.zone FROM deliveries d
			JOIN couriers c ON c.id = d.courier_id
			WHERE d.status='assigned' AND d.eta_at <= now() LIMIT 50 FOR UPDATE OF d SKIP LOCKED`)
		if err != nil {
			return err
		}
		type done struct {
			order, courier int64
			name, zone     string
		}
		var list []done
		for rows.Next() {
			var d done
			if err := rows.Scan(&d.order, &d.courier, &d.name, &d.zone); err != nil {
				rows.Close()
				return err
			}
			list = append(list, d)
		}
		rows.Close()
		for _, d := range list {
			if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='done', done_at=now() WHERE order_id=$1`, d.order); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE couriers SET busy=false, deliveries=deliveries+1 WHERE id=$1`, d.courier); err != nil {
				return err
			}
			if err := platform.Enqueue(ctx, tx, s.Name, events.TopicDelivery, events.DeliveryCompleted, d.order,
				events.DeliveryData{CourierID: d.courier, CourierName: d.name, Zone: d.zone}); err != nil {
				return err
			}
			s.completed.Inc(d.zone)
		}
		return nil
	})
}

func (s *service) gauges(ctx context.Context) error {
	var waiting int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE status='waiting'`).Scan(&waiting); err != nil {
		return err
	}
	s.waiting.Set(float64(waiting))
	rows, err := s.DB.QueryContext(ctx, `SELECT zone, count(*) FILTER (WHERE on_shift AND NOT busy) FROM couriers GROUP BY zone`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var zone string
		var n int
		if rows.Scan(&zone, &n) == nil {
			s.free.Set(float64(n), zone)
		}
	}
	return nil
}

type courier struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Zone       string `json:"zone"`
	Transport  string `json:"transport"`
	OnShift    bool   `json:"on_shift"`
	Busy       bool   `json:"busy"`
	Deliveries int    `json:"deliveries"`
}

func (s *service) list(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, name, zone, transport, on_shift, busy, deliveries FROM couriers ORDER BY zone, id`)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []courier{}
	for rows.Next() {
		var c courier
		if rows.Scan(&c.ID, &c.Name, &c.Zone, &c.Transport, &c.OnShift, &c.Busy, &c.Deliveries) == nil {
			out = append(out, c)
		}
	}
	platform.JSON(w, http.StatusOK, out)
}

var transports = map[string]bool{"пешком": true, "самокат": true, "велосипед": true, "автомобиль": true}

func (s *service) create(w http.ResponseWriter, r *http.Request) {
	if _, role := platform.User(r); role != auth.RoleAdmin {
		platform.Error(w, http.StatusForbidden, "admin role required")
		return
	}
	c := courier{OnShift: true}
	if err := platform.Decode(w, r, &c); err != nil {
		platform.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if l := len([]rune(strings.TrimSpace(c.Name))); l < 2 || l > 60 || !transports[c.Transport] ||
		(c.Zone != "center" && c.Zone != "north" && c.Zone != "south") {
		platform.Error(w, http.StatusBadRequest, "invalid name, zone or transport")
		return
	}
	err := s.DB.QueryRowContext(r.Context(), `INSERT INTO couriers(name, zone, transport, on_shift) VALUES ($1,$2,$3,$4) RETURNING id`,
		strings.TrimSpace(c.Name), c.Zone, c.Transport, c.OnShift).Scan(&c.ID)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "insert failed")
		return
	}
	platform.JSON(w, http.StatusCreated, c)
}

// setShift открывает или закрывает смену курьера. Занятый курьер
// завершает текущую доставку и после неё новых заказов не получает.
func (s *service) setShift(w http.ResponseWriter, r *http.Request) {
	id, ok := platform.PathID(r, "id")
	var req struct {
		OnShift *bool `json:"on_shift"`
	}
	if !ok || platform.Decode(w, r, &req) != nil || req.OnShift == nil {
		platform.Error(w, http.StatusBadRequest, "on_shift is required")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE couriers SET on_shift=$2 WHERE id=$1`, id, *req.OnShift)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "update failed")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		platform.Error(w, http.StatusNotFound, "courier not found")
		return
	}
	platform.JSON(w, http.StatusOK, map[string]any{"id": id, "on_shift": *req.OnShift})
}

func (s *service) deliveries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT d.order_id, d.zone, d.status, coalesce(c.name,''), d.attempts, d.created_at
		FROM deliveries d LEFT JOIN couriers c ON c.id = d.courier_id ORDER BY d.created_at DESC LIMIT 100`)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	type delivery struct {
		OrderID   int64     `json:"order_id"`
		Zone      string    `json:"zone"`
		Status    string    `json:"status"`
		Courier   string    `json:"courier"`
		Attempts  int       `json:"attempts"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []delivery{}
	for rows.Next() {
		var d delivery
		if rows.Scan(&d.OrderID, &d.Zone, &d.Status, &d.Courier, &d.Attempts, &d.CreatedAt) == nil {
			out = append(out, d)
		}
	}
	platform.JSON(w, http.StatusOK, out)
}
