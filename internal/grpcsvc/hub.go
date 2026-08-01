package grpcsvc

import (
	"log/slog"
	"sync"

	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// watchBuffer matches the order of magnitude upstream drivers use for their
// watch broadcast channels.
const watchBuffer = 128

// hub fans WatchSandboxesEvent values out to active watch streams. Slow
// subscribers drop events rather than block lifecycle work; the gateway
// recovers via its periodic ListSandboxes reconcile.
type hub struct {
	log  *slog.Logger
	mu   sync.Mutex
	subs map[int]chan *computev1.WatchSandboxesEvent
	next int
}

func newHub(log *slog.Logger) *hub {
	return &hub{log: log, subs: make(map[int]chan *computev1.WatchSandboxesEvent)}
}

func (h *hub) subscribe() (int, <-chan *computev1.WatchSandboxesEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.next
	h.next++
	ch := make(chan *computev1.WatchSandboxesEvent, watchBuffer)
	h.subs[id] = ch
	return id, ch
}

func (h *hub) unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(ch)
	}
}

func (h *hub) publish(ev *computev1.WatchSandboxesEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs {
		select {
		case ch <- ev:
		default:
			h.log.Warn("watch subscriber lagging, dropping event", "subscriber", id)
		}
	}
}
