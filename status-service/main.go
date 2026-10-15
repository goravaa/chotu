package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/goravaa/chotu/status-service/handlers"
	"github.com/goravaa/chotu/status-service/internals"

	"github.com/go-chi/chi/v5"
)

// version is stamped in at build time by the makefile.
var version = "dev"

// defaultAddr is the port the status task in the setup daemon maps. It is fixed
// on the container side because the host side is what the user's config
// chooses, and that mapping is set up where the config is read.
const defaultAddr = ":8081"

// readHeaderTimeout is set because the events route is a long-lived stream. The
// timeout has to be a header timeout specifically: a write deadline would cut
// the stream off mid-cycle.
const readHeaderTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	store := &internals.StatusStore{}
	hub := internals.NewStatusHub()
	keeper := internals.NewKeeper()

	statusHandler := handlers.NewStatusHandler(store, hub, keeper)
	eventHandler := handlers.NewEventHandler(store, hub)
	metaHandler := handlers.NewMetaHandler(keeper)

	r := chi.NewRouter()

	r.Get("/api/v1/status", statusHandler.GetStatus)
	r.Post("/api/v1/status", statusHandler.UpdateStatus)
	r.Get("/api/v1/events", eventHandler.Events)
	r.Get("/api/v1/meta", metaHandler.GetMeta)
	r.Get("/", handlers.Page)

	addr := os.Getenv("CHOTU_STATUS_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// A container stop is SIGTERM, and without this the process would be killed
	// mid-write. The events stream is the thing that loses data, since a
	// browser reconnecting mid-cycle should not have to wait for the next one.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-shutdown

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("chotu-status %s listening on %s", version, addr)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	log.Print("chotu-status stopped")

	return nil
}
