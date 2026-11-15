// Сервис оплаты: авторизация и возврат платежей по событиям заказов.
package main

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"net/http"
	"time"

	"fooddelivery/internal/events"
	"fooddelivery/internal/metrics"
	"fooddelivery/internal/platform"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type service struct {
	*platform.App
	limit    int64
	payments *metrics.Counter
	amount   *metrics.Counter
}

func main() {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	app := platform.Start("payment-service", platform.MergeFS(sub, "000_outbox.sql", platform.OutboxSchema), 103, true)
	s := &service{
		App:      app,
		limit:    int64(platform.EnvInt("PAYMENT_LIMIT", 5000)),
		payments: app.Server.Metrics.NewCounter("payments_total", "Payments by result", "status"),
		amount:   app.Server.Metrics.NewCounter("payments_amount_rub_total", "Authorized amount, rub"),
	}
	app.Server.Mux.HandleFunc("GET /payments", s.list)
	app.Run(
		app.Relay(),
		app.Consumer("payment-service", []string{events.TopicOrders}, s.onEvent),
	)
}

func (s *service) onEvent(ctx context.Context, ev events.Event) error {
	switch ev.Type {
	case events.OrderCreated:
		return s.authorize(ctx, ev)
	case events.OrderCancelled:
		return s.refund(ctx, ev)
	}
	return nil
}

func (s *service) authorize(ctx context.Context, ev events.Event) error {
	var d events.OrderCreatedData
	if err := ev.Decode(&d); err != nil {
		return err
	}
	dec := decide(d.Total, s.limit)
	status, eventType := "authorized", events.PaymentAuthorized
	if !dec.Approved {
		status, eventType = "declined", events.PaymentDeclined
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		first, err := platform.FirstTime(ctx, tx, ev.ID)
		if err != nil || !first {
			return err
		}
		var id int64
		err = tx.QueryRowContext(ctx, `INSERT INTO payments(order_id, amount, status, reason) VALUES ($1,$2,$3,$4)
			ON CONFLICT (order_id) DO NOTHING RETURNING id`,
			ev.OrderID, d.Total, status, sql.NullString{String: dec.Reason, Valid: dec.Reason != ""}).Scan(&id)
		if err == sql.ErrNoRows {
			return nil // платёж по заказу уже создан
		} else if err != nil {
			return err
		}
		s.payments.Inc(status)
		if dec.Approved {
			s.amount.Add(float64(d.Total))
		}
		return platform.Enqueue(ctx, tx, s.Name, events.TopicPayments, eventType, ev.OrderID, events.PaymentData{
			PaymentID: id, Amount: d.Total, Reason: dec.Reason,
			RestaurantID: d.RestaurantID, Zone: d.Zone, PrepSeconds: d.PrepSeconds,
		})
	})
}

func (s *service) refund(ctx context.Context, ev events.Event) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		first, err := platform.FirstTime(ctx, tx, ev.ID)
		if err != nil || !first {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE payments SET status='refunded', updated_at=now()
			WHERE order_id=$1 AND status='authorized'`, ev.OrderID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			s.payments.Inc("refunded")
		}
		return nil
	})
}

func (s *service) list(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, order_id, amount, status, coalesce(reason,''), created_at
		FROM payments ORDER BY id DESC LIMIT 200`)
	if err != nil {
		platform.Error(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	type payment struct {
		ID        int64     `json:"id"`
		OrderID   int64     `json:"order_id"`
		Amount    int64     `json:"amount"`
		Status    string    `json:"status"`
		Reason    string    `json:"reason"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []payment{}
	for rows.Next() {
		var p payment
		if rows.Scan(&p.ID, &p.OrderID, &p.Amount, &p.Status, &p.Reason, &p.CreatedAt) == nil {
			out = append(out, p)
		}
	}
	platform.JSON(w, http.StatusOK, out)
}
