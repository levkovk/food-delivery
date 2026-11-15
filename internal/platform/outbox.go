package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"fooddelivery/internal/events"
)

// OutboxSchema - таблицы, общие для всех сервисов, которые публикуют и
// получают события: исходящий буфер и журнал обработанных событий.
const OutboxSchema = `
CREATE TABLE IF NOT EXISTS outbox (
    id         bigserial PRIMARY KEY,
    topic      text        NOT NULL,
    event      jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS processed_events (
    event_id     text PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);`

// Execer - общий интерфейс *sql.DB и *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Enqueue записывает событие в исходящий буфер в той же транзакции, что и
// изменение данных. Так событие не теряется, если брокер недоступен,
// и не уходит, если транзакция откатилась (шаблон transactional outbox).
// Идентификатор события назначается здесь же, поэтому при повторной
// отправке получатель узнает дубликат.
func Enqueue(ctx context.Context, tx Execer, source, topic, eventType string, orderID int64, data any) error {
	ev, err := events.New(eventType, source, orderID, data)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox(topic, event) VALUES ($1,$2)`, topic, raw)
	return err
}

// FirstTime отмечает событие как обработанное и возвращает false, если оно уже
// встречалось. Повторная доставка из Kafka тогда не меняет данные дважды.
func FirstTime(ctx context.Context, tx *sql.Tx, eventID string) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO processed_events(event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RelayOutbox периодически переносит события из буфера в Kafka. Строки
// блокируются через SKIP LOCKED, поэтому несколько реплик не публикуют одно
// событие одновременно.
func RelayOutbox(db *sql.DB, pub *events.Publisher, log *slog.Logger) func(context.Context) error {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
			if err := relayBatch(ctx, db, pub); err != nil && ctx.Err() == nil {
				log.Warn("outbox relay failed", "err", err)
			}
		}
	}
}

func relayBatch(ctx context.Context, db *sql.DB, pub *events.Publisher) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.QueryContext(ctx, `SELECT id, topic, event FROM outbox
		ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	type item struct {
		id    int64
		topic string
		event []byte
	}
	var batch []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.topic, &it.event); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, it)
	}
	rows.Close()
	if len(batch) == 0 {
		return nil
	}
	for _, it := range batch {
		var ev events.Event
		if err := json.Unmarshal(it.event, &ev); err != nil {
			return err
		}
		if err := pub.Write(ctx, it.topic, ev); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE id=$1`, it.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
