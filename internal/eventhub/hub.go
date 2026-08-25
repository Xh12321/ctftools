package eventhub

import (
	"sync"

	"github.com/Xh12321/ctftools/internal/platform"
)

// Hub fans out task events to live subscribers and supports catch-up via
// sequence numbers (subscribers always filter with after on their side).
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan platform.TaskEvent]struct{}
}

// New creates an empty event hub.
func New() *Hub {
	return &Hub{
		subs: make(map[string]map[chan platform.TaskEvent]struct{}),
	}
}

// Subscribe registers a buffered channel for taskID events.
// The caller must call the returned cancel func to release resources.
// Events with Sequence <= after are not delivered (caller should load history
// from storage first, then subscribe with after = last seen sequence).
func (h *Hub) Subscribe(taskID string, after int64, buffer int) (<-chan platform.TaskEvent, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan platform.TaskEvent, buffer)

	h.mu.Lock()
	if h.subs[taskID] == nil {
		h.subs[taskID] = make(map[chan platform.TaskEvent]struct{})
	}
	h.subs[taskID][ch] = struct{}{}
	h.mu.Unlock()

	// Wrap channel to filter by sequence. We do filtering on publish to keep
	// the receive side simple; store after on a side map.
	// Actually: use a filter wrapper via dedicated sub record.
	// Simpler approach: publish checks nothing; Subscribe returns raw ch and
	// we document that publisher only sends new events. Catch-up is storage.
	// For safety against races, filter in a pump goroutine is overkill;
	// store after and filter on Publish.
	_ = after

	h.setAfter(ch, after)

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if set, ok := h.subs[taskID]; ok {
				if _, exists := set[ch]; exists {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(h.subs, taskID)
				}
			}
			h.mu.Unlock()
			h.clearMeta(ch)
		})
	}

	return ch, cancel
}

type subMeta struct {
	after int64
}

var metaMu sync.Mutex
var meta = map[chan platform.TaskEvent]subMeta{}

func (h *Hub) setAfter(ch chan platform.TaskEvent, after int64) {
	metaMu.Lock()
	meta[ch] = subMeta{after: after}
	metaMu.Unlock()
}

func (h *Hub) clearMeta(ch chan platform.TaskEvent) {
	metaMu.Lock()
	delete(meta, ch)
	metaMu.Unlock()
}

func getAfter(ch chan platform.TaskEvent) int64 {
	metaMu.Lock()
	defer metaMu.Unlock()
	return meta[ch].after
}

// Publish delivers an event to all subscribers of its task.
// Slow subscribers that fill their buffer drop the event (they can catch up
// via storage using after). Publish never blocks.
func (h *Hub) Publish(ev platform.TaskEvent) {
	h.mu.RLock()
	set := h.subs[ev.TaskID]
	// Copy channel list under lock.
	chans := make([]chan platform.TaskEvent, 0, len(set))
	for ch := range set {
		chans = append(chans, ch)
	}
	h.mu.RUnlock()

	for _, ch := range chans {
		if ev.Sequence <= getAfter(ch) {
			continue
		}
		select {
		case ch <- ev:
		default:
			// Drop; subscriber is too slow.
		}
	}
}

// PublishAll publishes a batch of events in order.
func (h *Hub) PublishAll(events []platform.TaskEvent) {
	for _, ev := range events {
		h.Publish(ev)
	}
}

// SubscriberCount returns the number of live subscribers for a task (testing).
func (h *Hub) SubscriberCount(taskID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[taskID])
}

// Close unsubscribes everyone. Safe to call multiple times.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for taskID, set := range h.subs {
		for ch := range set {
			close(ch)
			h.clearMeta(ch)
		}
		delete(h.subs, taskID)
	}
}
