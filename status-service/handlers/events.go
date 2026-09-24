package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/goravaa/chotu/status-service/internals"
)

type EventHandler struct {
	hub *internals.StatusHub
}

func NewEventHandler(hub *internals.StatusHub) *EventHandler {
	return &EventHandler{
		hub: hub,
	}
}

func (h *EventHandler) Events(w http.ResponseWriter, r *http.Request) {
	client := h.hub.AddClient()
	defer h.hub.RemoveClient(client)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for {
		select {
		case status := <-client:
			data, err := json.Marshal(status)
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
