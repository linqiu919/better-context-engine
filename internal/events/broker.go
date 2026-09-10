package events

import (
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Data      any       `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}

type Broker struct {
	mu      sync.RWMutex
	nextID  uint64
	clients map[chan Event]struct{}
}

func New() *Broker { return &Broker{clients: map[chan Event]struct{}{}} }

func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.clients[ch]; ok {
			delete(b.clients, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

func (b *Broker) Publish(kind string, data any) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	event := Event{ID: encodeID(id), Type: kind, Data: data, CreatedAt: time.Now()}
	for ch := range b.clients {
		select {
		case ch <- event:
		default:
		}
	}
	b.mu.Unlock()
}

func encodeID(value uint64) string { raw, _ := json.Marshal(value); return string(raw) }
