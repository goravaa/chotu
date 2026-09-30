package internals

import (
	"sync"

	"github.com/goravaa/chotu/status"
)

// StatusHub fans a published document out to every connected browser.
//
// Each client gets a buffered channel of depth one. A slow client therefore
// loses intermediate updates instead of stalling the publisher, which matters
// because the publisher is the setup daemon and must never block on a browser.
type StatusHub struct {
	mu      sync.RWMutex
	clients map[chan status.Status]struct{}
}

func NewStatusHub() *StatusHub {
	return &StatusHub{
		clients: make(map[chan status.Status]struct{}),
	}
}

func (h *StatusHub) AddClient() chan status.Status {
	client := make(chan status.Status, 1)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.clients[client] = struct{}{}

	return client
}

func (h *StatusHub) RemoveClient(client chan status.Status) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.clients, client)
	close(client)
}

func (h *StatusHub) Broadcast(status status.Status) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		select {
		case client <- status:
		default:
			// Don't block the status update if a client
			// hasn't consumed the previous update yet.
		}
	}
}
