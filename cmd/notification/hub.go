package main

import (
	"sync"

	"fooddelivery/internal/events"
)

// hub хранит последние события и рассылает новые подписчикам.
type hub struct {
	mu     sync.Mutex
	recent []events.Event
	limit  int
	subs   map[chan events.Event]struct{}
}

func newHub(limit int) *hub {
	return &hub{limit: limit, subs: map[chan events.Event]struct{}{}}
}

// publish сохраняет событие в кольцевом буфере и рассылает его. Медленный
// подписчик с заполненным каналом пропускает событие, а не тормозит остальных.
func (h *hub) publish(ev events.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recent = append(h.recent, ev)
	if len(h.recent) > h.limit {
		h.recent = h.recent[len(h.recent)-h.limit:]
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (h *hub) subscribe() (chan events.Event, func()) {
	ch := make(chan events.Event, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *hub) snapshot() []events.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]events.Event, len(h.recent))
	copy(out, h.recent)
	return out
}

func (h *hub) subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
