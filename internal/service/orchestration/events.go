package orchestration

import (
	"sync"

	"codescan/internal/model"
)

type eventHub struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan model.TaskEvent]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{
		subscribers: map[string]map[chan model.TaskEvent]struct{}{},
	}
}

func (h *eventHub) publish(event model.TaskEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for ch := range h.subscribers[event.TaskID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (h *eventHub) subscribe(taskID string) (<-chan model.TaskEvent, func()) {
	ch := make(chan model.TaskEvent, 64)

	h.mu.Lock()
	if h.subscribers[taskID] == nil {
		h.subscribers[taskID] = map[chan model.TaskEvent]struct{}{}
	}
	h.subscribers[taskID][ch] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if group := h.subscribers[taskID]; group != nil {
			delete(group, ch)
			if len(group) == 0 {
				delete(h.subscribers, taskID)
			}
		}
		close(ch)
	}

	return ch, cancel
}
