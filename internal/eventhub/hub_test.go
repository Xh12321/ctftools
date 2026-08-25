package eventhub_test

import (
	"testing"
	"time"

	"github.com/Xh12321/ctftools/internal/eventhub"
	"github.com/Xh12321/ctftools/internal/platform"
)

func TestSubscribePublishAndAfterFilter(t *testing.T) {
	h := eventhub.New()
	defer h.Close()

	ch, cancel := h.Subscribe("task-1", 2, 8)
	defer cancel()

	// sequence 2 should be filtered (after=2 means >2)
	h.Publish(platform.TaskEvent{TaskID: "task-1", Sequence: 2, Type: "old"})
	h.Publish(platform.TaskEvent{TaskID: "task-1", Sequence: 3, Type: "new"})
	h.Publish(platform.TaskEvent{TaskID: "task-other", Sequence: 1, Type: "nope"})

	select {
	case ev := <-ch:
		if ev.Sequence != 3 || ev.Type != "new" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	select {
	case ev := <-ch:
		t.Fatalf("unexpected extra event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCancelClosesChannel(t *testing.T) {
	h := eventhub.New()
	defer h.Close()
	ch, cancel := h.Subscribe("t", 0, 1)
	cancel()
	_, ok := <-ch
	if ok {
		t.Fatal("channel should be closed")
	}
	if h.SubscriberCount("t") != 0 {
		t.Fatal("subscriber should be gone")
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	h := eventhub.New()
	defer h.Close()
	// buffer 1
	ch, cancel := h.Subscribe("t", 0, 1)
	defer cancel()

	h.Publish(platform.TaskEvent{TaskID: "t", Sequence: 1})
	h.Publish(platform.TaskEvent{TaskID: "t", Sequence: 2}) // may drop
	h.Publish(platform.TaskEvent{TaskID: "t", Sequence: 3}) // may drop

	// Drain whatever is there without blocking forever.
	got := 0
	for {
		select {
		case <-ch:
			got++
		case <-time.After(50 * time.Millisecond):
			if got < 1 {
				t.Fatal("expected at least one event")
			}
			return
		}
	}
}
