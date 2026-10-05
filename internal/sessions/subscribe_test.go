package sessions

import (
	"context"
	"testing"
)

func TestSubscribeGetsEventsInOrderAndStopsWithItsContext(t *testing.T) {
	m := NewManager(context.Background(), Config{})
	ctx, cancel := context.WithCancel(context.Background())
	snapshot, events, err := m.Subscribe(ctx)
	if err != nil || len(snapshot.Sessions) != 0 {
		t.Fatalf("snapshot = %+v, err = %v", snapshot, err)
	}
	m.mu.Lock()
	m.publish(&Event{Kind: EventNotice, Notice: "one"})
	m.publish(&Event{Kind: EventNotice, Notice: "two"})
	m.mu.Unlock()

	first, second := <-events, <-events
	if first.Notice != "one" || second.Notice != "two" || first.Seq <= snapshot.Seq || second.Seq != first.Seq+1 {
		t.Fatalf("events = %+v, %+v", first, second)
	}
	cancel()
	if _, open := <-events; open {
		t.Fatal("the channel must close with the subscription's context")
	}
}

func TestLaggingSubscriberGetsResync(t *testing.T) {
	m := NewManager(context.Background(), Config{})
	_, events, err := m.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for range subscriberBuffer + 5 {
		m.publish(&Event{Kind: EventNotice})
	}
	m.mu.Unlock()
	for range subscriberBuffer {
		<-events
	}
	m.mu.Lock()
	m.publish(&Event{Kind: EventNotice, Notice: "after"})
	m.mu.Unlock()
	if ev := <-events; ev.Kind != EventResync {
		t.Fatalf("event = %+v, want a resync", ev)
	}
}
