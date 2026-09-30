package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/goravaa/chotu/status-service/internals"
)

type EventHandler struct {
	store *internals.StatusStore
	hub   *internals.StatusHub
}

func NewEventHandler(
	store *internals.StatusStore, hub *internals.StatusHub) *EventHandler {
	return &EventHandler{
		store: store,
		hub:   hub,
	}
}

// Events streams every published document to the browser over SSE, so the page
// updates when the setup daemon finishes a cycle rather than on a poll.
func (h *EventHandler) Events(w http.ResponseWriter, r *http.Request) {
	client := h.hub.AddClient()
	defer h.hub.RemoveClient(client)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send the current state immediately. A page opened between cycles would
	// otherwise sit on "Waiting for status..." until the next one finished.
	if current, err := json.Marshal(h.store.Get()); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", current)
		w.(http.Flusher).Flush()
	}

	for {
		select {
		case update := <-client:
			data, err := json.Marshal(update)
			if err != nil {
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()

		case <-r.Context().Done():
			return
		}
	}
}
