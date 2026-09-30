package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/goravaa/chotu/status"
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
	// Checked once, up front. An SSE response that is never flushed is worse
	// than an error, so this is not something to discover by panicking on the
	// first write: a wrapping middleware can legitimately hand over a
	// ResponseWriter that does not implement Flusher.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)

		return
	}

	client := h.hub.AddClient()
	defer h.hub.RemoveClient(client)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send the current state immediately. A page opened between cycles would
	// otherwise sit on "Waiting for status..." until the next one finished.
	if err := send(w, flusher, h.store.Get()); err != nil {
		return
	}

	for {
		select {
		case update := <-client:
			if err := send(w, flusher, update); err != nil {
				// The client is gone. Waiting for the request context to be
				// cancelled would work, but TCP accepts writes into a buffer
				// after the peer has vanished, so a failed write is the earlier
				// and more reliable signal that there is nobody left to read.
				return
			}

		case <-r.Context().Done():
			return
		}
	}
}

// send writes one SSE frame and flushes it. Flushing per frame is the whole
// point: without it the response sits in the proxy's buffer and the page only
// updates when the connection happens to close.
func send(w http.ResponseWriter, flusher http.Flusher, document status.Status) error {
	data, err := json.Marshal(document)
	if err != nil {
		// A document that cannot be encoded is skipped rather than fatal, so
		// one bad report does not take the stream down for every client.
		log.Printf("[events] could not encode a status document: %v", err)

		return nil
	}

	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}

	flusher.Flush()

	return nil
}
