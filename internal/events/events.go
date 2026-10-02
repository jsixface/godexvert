// Package events fans out server-sent events to connected clients.
package events

import "sync"

// Event is one SSE message. Data may span multiple lines.
type Event struct {
	Name string
	Data string
}

// Broker delivers published events to all subscribers. Slow subscribers drop events
// rather than blocking publishers.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

// Subscribe returns a channel of events and a func to unsubscribe.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribers returns the number of connected clients.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
