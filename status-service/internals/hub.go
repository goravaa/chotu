package internals

import "sync"

type StatusHub struct {
	mu      sync.RWMutex
	clients map[chan Status]struct{}
}

func NewStatusHub() *StatusHub {
	return &StatusHub{
		clients: make(map[chan Status]struct{}),
	}
}

func (h *StatusHub) AddClient() chan Status {
	client := make(chan Status, 1)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.clients[client] = struct{}{}

	return client
}

func (h *StatusHub) RemoveClient(client chan Status) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.clients, client)
	close(client)
}

func (h *StatusHub) Broadcast(status Status) {
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
