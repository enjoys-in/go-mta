package events

import (
	"log/slog"
	"sync"

	"github.com/enjoys-in/go-mta/pkg/logger"
)

// HandlerFunc is a callback invoked when an event fires.
type HandlerFunc func(Event)

// Handler dispatches events to registered listeners.
type Handler struct {
	events   chan Event
	mu       sync.RWMutex
	handlers map[string][]HandlerFunc
	log      *logger.Logger
	done     chan struct{}
	once     sync.Once
}

// NewHandler creates an event handler with the given channel buffer size.
func NewHandler(bufSize int) *Handler {
	if bufSize <= 0 {
		bufSize = 256
	}
	return &Handler{
		events:   make(chan Event, bufSize),
		handlers: make(map[string][]HandlerFunc),
		log:      logger.New("events"),
		done:     make(chan struct{}),
	}
}

// On registers a listener for the given event type.
func (h *Handler) On(eventType string, fn HandlerFunc) {
	h.mu.Lock()
	h.handlers[eventType] = append(h.handlers[eventType], fn)
	h.mu.Unlock()
}

// Emit sends an event into the dispatch channel (non-blocking drop if full).
func (h *Handler) Emit(evt Event) {
	select {
	case h.events <- evt:
	default:
		h.log.Warn("event channel full, dropping event",
			slog.String("event_type", evt.Type),
			slog.String("job_id", evt.JobID),
		)
	}
}

// Dispatch runs the event loop. Call this in a goroutine.
// It exits when Stop() is called.
func (h *Handler) Dispatch() {
	for evt := range h.events {
		h.mu.RLock()
		listeners := h.handlers[evt.Type]
		h.mu.RUnlock()

		for _, fn := range listeners {
			fn(evt) // run synchronously per listener; caller can go fn(evt) if needed
		}
	}
	close(h.done)
}

// Stop closes the event channel and waits for Dispatch to drain.
func (h *Handler) Stop() {
	h.once.Do(func() {
		close(h.events)
		<-h.done
	})
}
