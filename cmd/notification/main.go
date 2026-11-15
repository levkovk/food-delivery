// Сервис уведомлений: читает все события и передаёт их в панель оператора
// по протоколу Server-Sent Events.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"fooddelivery/internal/events"
	"fooddelivery/internal/metrics"
	"fooddelivery/internal/platform"
)

type service struct {
	*platform.App
	hub      *hub
	received *metrics.Counter
	clients  *metrics.Gauge
}

func main() {
	app := platform.Start("notification-service", nil, 0, true)
	s := &service{
		App:      app,
		hub:      newHub(100),
		received: app.Server.Metrics.NewCounter("notifications_events_total", "Events received", "type"),
		clients:  app.Server.Metrics.NewGauge("notifications_stream_clients", "Connected stream clients"),
	}
	app.Server.Mux.HandleFunc("GET /recent", s.recent)
	app.Server.Mux.HandleFunc("GET /stream", s.stream)
	// Каждая реплика читает поток целиком в своей группе потребителей:
	// подписчик может быть подключён к любой реплике.
	group := "notification-" + platform.Hostname()
	app.Run(app.Consumer(group, events.AllTopics, func(_ context.Context, ev events.Event) error {
		s.received.Inc(ev.Type)
		s.hub.publish(ev)
		return nil
	}))
}

func (s *service) recent(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, s.hub.snapshot())
}

func (s *service) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		platform.Error(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := s.hub.subscribe()
	defer cancel()
	s.clients.Set(float64(s.hub.subscribers()))
	defer func() { s.clients.Set(float64(s.hub.subscribers() - 1)) }()
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			body, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, body)
			flusher.Flush()
		}
	}
}
