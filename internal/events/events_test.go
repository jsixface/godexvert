package events

import "testing"

func TestBroker(t *testing.T) {
	b := NewBroker()
	ch, unsub := b.Subscribe()
	b.Publish(Event{Name: "x", Data: "1"})
	if e := <-ch; e.Name != "x" || e.Data != "1" {
		t.Fatalf("got %+v", e)
	}
	// A full subscriber must not block publishers.
	for range 100 {
		b.Publish(Event{Name: "y"})
	}
	unsub()
	if b.Subscribers() != 0 {
		t.Fatal("unsubscribe")
	}
	b.Publish(Event{Name: "z"})
}
