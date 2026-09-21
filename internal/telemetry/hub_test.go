package telemetry

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscriptionReportsOverflow(t *testing.T) {
	hub := NewHub()
	events, overflowed, cancel := hub.SubscribeWithOverflow()
	defer cancel()

	for i := 0; i < subscriberBuffer+1; i++ {
		hub.Publish(Event{Type: "request_queued", Payload: map[string]any{"index": i}})
	}
	hub.Flush()
	if !overflowed() {
		t.Fatal("expected bounded subscription to report overflow")
	}
	if overflowed() {
		t.Fatal("expected overflow probe to reset after it is observed")
	}
	if len(events) != subscriberBuffer {
		t.Fatalf("expected buffer to remain bounded at %d events, got %d", subscriberBuffer, len(events))
	}
}

func TestScopedPublishRoutesBeforeEncodingAndPreparesOncePerGroup(t *testing.T) {
	hub := NewHub()
	hub.SetAudienceResolver(func(event Event) Audience {
		return Audience{Scoped: true, Partition: event.Payload["partition"].(string)}
	})
	var encodes atomic.Int64
	encoder := func(event Event) ([]byte, bool, error) {
		encodes.Add(1)
		payload, err := json.Marshal(event)
		return payload, true, err
	}
	a1, _, cancelA1, cursorA1 := hub.SubscribePrepared(SubscriptionScope{Partition: "a", Format: "browser"}, encoder)
	defer cancelA1()
	a2, _, cancelA2, cursorA2 := hub.SubscribePrepared(SubscriptionScope{Partition: "a", Format: "browser"}, encoder)
	defer cancelA2()
	b, _, cancelB, _ := hub.SubscribePrepared(SubscriptionScope{Partition: "b", Format: "browser"}, encoder)
	defer cancelB()
	if cursorA1 != cursorA2 {
		t.Fatalf("expected shared group cursor, got %#v and %#v", cursorA1, cursorA2)
	}

	hub.Publish(Event{Type: "request_queued", Payload: map[string]any{"partition": "a"}})
	hub.Flush()
	first := <-a1
	second := <-a2
	if string(first) != string(second) {
		t.Fatal("expected one prepared payload to be shared by the group")
	}
	if encodes.Load() != 1 {
		t.Fatalf("expected one encode for the matching group, got %d", encodes.Load())
	}
	select {
	case payload := <-b:
		t.Fatalf("unexpected cross-partition delivery %s", payload)
	default:
	}
}

func TestSubjectOnlyPublishReachesManagersAndMatchingSubject(t *testing.T) {
	hub := NewHub()
	hub.SetAudienceResolver(func(event Event) Audience {
		return Audience{Scoped: true, Partition: "a", Subject: "user-a", SubjectOnly: true}
	})
	manager, _, cancelManager, _ := hub.SubscribePrepared(SubscriptionScope{Partition: "a", Format: "browser"}, nil)
	defer cancelManager()
	actorA, _, cancelActorA, _ := hub.SubscribePrepared(SubscriptionScope{Partition: "a", Subject: "user-a", Format: "browser"}, nil)
	defer cancelActorA()
	actorB, _, cancelActorB, _ := hub.SubscribePrepared(SubscriptionScope{Partition: "a", Subject: "user-b", Format: "browser"}, nil)
	defer cancelActorB()

	hub.Publish(Event{Type: "request_queued"})
	hub.Flush()
	<-manager
	<-actorA
	select {
	case payload := <-actorB:
		t.Fatalf("unexpected other-subject delivery %s", payload)
	default:
	}
}

func TestPublishDoesNotWaitForSlowEncoding(t *testing.T) {
	hub := NewHub()
	_, _, cancel, _ := hub.SubscribePrepared(SubscriptionScope{Format: "slow"}, func(event Event) ([]byte, bool, error) {
		time.Sleep(100 * time.Millisecond)
		return []byte(`{}`), true, nil
	})
	defer cancel()
	started := time.Now()
	hub.Publish(Event{Type: "request_queued"})
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Fatalf("publish waited for subscriber work: %s", elapsed)
	}
	hub.Flush()
}

func TestCancelDoesNotRaceWithPublish(t *testing.T) {
	hub := NewHub()
	_, _, cancel := hub.SubscribeWithOverflow()
	cancel()

	// Publishing after cancellation must not send to a closed subscriber.
	hub.Publish(Event{Type: "request_queued"})
}
