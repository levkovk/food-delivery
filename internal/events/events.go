// Package events описывает доменные события и работу с Kafka.
package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

// Топики Kafka. Ключ сообщения - идентификатор заказа, поэтому все события
// одного заказа попадают в одну партицию и читаются по порядку.
const (
	TopicOrders   = "orders"
	TopicPayments = "payments"
	TopicKitchen  = "kitchen"
	TopicDelivery = "delivery"
)

// AllTopics - все топики системы.
var AllTopics = []string{TopicOrders, TopicPayments, TopicKitchen, TopicDelivery}

// Типы событий.
const (
	OrderCreated       = "order.created"
	OrderCancelled     = "order.cancelled"
	PaymentAuthorized  = "payment.authorized"
	PaymentDeclined    = "payment.declined"
	KitchenAccepted    = "kitchen.accepted"
	KitchenReady       = "kitchen.ready"
	CourierAssigned    = "courier.assigned"
	DeliveryCompleted  = "delivery.completed"
	DeliveryNoCouriers = "delivery.waiting"
)

// Event - конверт, в котором передаётся любое событие.
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	OrderID int64           `json:"order_id"`
	At      time.Time       `json:"at"`
	Source  string          `json:"source"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// New создаёт событие с уникальным идентификатором.
func New(eventType, source string, orderID int64, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return Event{}, err
	}
	return Event{ID: hex.EncodeToString(id), Type: eventType, OrderID: orderID, At: time.Now().UTC(), Source: source, Data: raw}, nil
}

// Decode разбирает данные события в структуру.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// Payload события order.created.
type OrderCreatedData struct {
	RestaurantID int64  `json:"restaurant_id"`
	Total        int64  `json:"total"`
	Zone         string `json:"zone"`
	PrepSeconds  int    `json:"prep_seconds"`
	Address      string `json:"address"`
}

// Payload событий оплаты. Сведения о ресторане пересылаются из
// order.created, чтобы кухне не нужно было обращаться к сервису заказов.
type PaymentData struct {
	PaymentID    int64  `json:"payment_id"`
	Amount       int64  `json:"amount"`
	Reason       string `json:"reason,omitempty"`
	RestaurantID int64  `json:"restaurant_id,omitempty"`
	Zone         string `json:"zone,omitempty"`
	PrepSeconds  int    `json:"prep_seconds,omitempty"`
}

// Payload события kitchen.ready.
type KitchenData struct {
	RestaurantID int64  `json:"restaurant_id"`
	Zone         string `json:"zone"`
	PrepSeconds  int    `json:"prep_seconds,omitempty"`
}

// Payload событий доставки.
type DeliveryData struct {
	CourierID   int64  `json:"courier_id,omitempty"`
	CourierName string `json:"courier_name,omitempty"`
	Zone        string `json:"zone"`
	Attempt     int    `json:"attempt,omitempty"`
}

// Brokers разбирает список адресов брокеров через запятую.
func Brokers(list string) []string {
	var out []string
	for _, b := range strings.Split(list, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// EnsureTopics создаёт недостающие топики. Вызов идемпотентен, поэтому его
// безопасно выполнять при старте каждой реплики.
func EnsureTopics(ctx context.Context, brokers []string, partitions, replication int) error {
	if len(brokers) == 0 {
		return errors.New("no kafka brokers")
	}
	var dialer kafka.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", brokers[0])
	if err != nil {
		return err
	}
	defer conn.Close()
	controller, err := conn.Controller()
	if err != nil {
		return err
	}
	cc, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return err
	}
	defer cc.Close()
	cfgs := make([]kafka.TopicConfig, 0, len(AllTopics))
	for _, t := range AllTopics {
		cfgs = append(cfgs, kafka.TopicConfig{Topic: t, NumPartitions: partitions, ReplicationFactor: replication})
	}
	return cc.CreateTopics(cfgs...)
}

// Publisher отправляет события в Kafka.
type Publisher struct {
	w *kafka.Writer
}

// NewPublisher создаёт отправителя, ожидающего подтверждения от брокера.
func NewPublisher(brokers []string) *Publisher {
	return &Publisher{
		w: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll,
			BatchTimeout: 10 * time.Millisecond,
		},
	}
}

// Write записывает готовое событие в топик. Ключом служит номер заказа.
func (p *Publisher) Write(ctx context.Context, topic string, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return p.w.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Key:   []byte(strconv.FormatInt(ev.OrderID, 10)),
		Value: body,
	})
}

// Close закрывает соединения отправителя.
func (p *Publisher) Close() error { return p.w.Close() }

// maxAttempts - число попыток обработки одного события до его пропуска.
const maxAttempts = 20

// Handler обрабатывает одно событие. Ошибка оставляет смещение
// незафиксированным, и сообщение будет прочитано повторно.
type Handler func(ctx context.Context, ev Event) error

// Consume читает топики в составе группы потребителей до отмены контекста.
func Consume(ctx context.Context, log *slog.Logger, brokers []string, group string, topics []string, h Handler) error {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        brokers,
		GroupID:        group,
		GroupTopics:    topics,
		MinBytes:       1,
		MaxBytes:       1 << 20,
		MaxWait:        500 * time.Millisecond,
		StartOffset:    kafka.FirstOffset,
		CommitInterval: 0,
	})
	defer r.Close()
	for {
		msg, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("fetch: %w", err)
		}
		var ev Event
		if err := json.Unmarshal(msg.Value, &ev); err != nil {
			log.Warn("skip malformed message", "topic", msg.Topic, "offset", msg.Offset, "err", err)
		} else {
			for attempt := 1; ; attempt++ {
				err = h(ctx, ev)
				if err == nil || ctx.Err() != nil {
					break
				}
				if attempt == maxAttempts {
					log.Error("handler gave up, event skipped", "type", ev.Type, "order", ev.OrderID, "event", ev.ID, "err", err)
					break
				}
				delay := time.Duration(min(attempt, 10)) * 500 * time.Millisecond
				log.Warn("handler failed, retrying", "type", ev.Type, "order", ev.OrderID, "attempt", attempt, "err", err)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
				}
			}
		}
		if err := r.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
			log.Warn("commit failed", "err", err)
		}
	}
}
