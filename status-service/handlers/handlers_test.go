package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goravaa/chotu/status"
	"github.com/goravaa/chotu/status-service/internals"

	"github.com/go-chi/chi/v5"
)

// newRouter builds the same routes main.go does. Using the real router rather
// than a hand-rolled one means the wiring is covered too: a GET and a POST on
// the same path are the whole point of this API, and ServeMux cannot express it.
func newRouter() http.Handler {
	return newRouterWithKeeper(internals.NewKeeper())
}

func newRouterWithKeeper(keeper *internals.Keeper) http.Handler {
	store := &internals.StatusStore{}
	hub := internals.NewStatusHub()

	statusHandler := NewStatusHandler(store, hub, keeper)
	eventHandler := NewEventHandler(store, hub)
	metaHandler := NewMetaHandler(keeper)

	r := chi.NewRouter()

	r.Get("/api/v1/status", statusHandler.GetStatus)
	r.Post("/api/v1/status", statusHandler.UpdateStatus)
	r.Get("/api/v1/events", eventHandler.Events)
	r.Get("/api/v1/meta", metaHandler.GetMeta)
	r.Get("/", Page)

	return r
}

func get(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

	return recorder
}

// TestGetStatusBeforeAnyReport is the state a user sees after starting the
// container but before the daemon's first cycle finishes. It has to be a
// decodable document rather than a 404 or an error page, because on a slow
// first boot that is the only thing on screen.
func TestGetStatusBeforeAnyReport(t *testing.T) {
	recorder := get(t, newRouter(), "/api/v1/status")

	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q, want application/json", got)
	}

	var document status.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("the empty document must be valid JSON: %v", err)
	}

	if document.Cycle != 0 {
		t.Errorf("cycle = %d, want 0 before any report", document.Cycle)
	}
}

// TestPostThenGet round-trips the daemon's document, which is the whole reason
// this service exists. A field the encoder and decoder disagree on would show up
// here rather than in a browser.
func TestPostThenGet(t *testing.T) {
	router := newRouter()

	published := *status.New()
	published.Timestamp = "2026-09-30T08:00:00Z"
	published.Version = "v1.2.3"
	published.State = "FAILED"
	published.Cycle = 7
	published.Setup.LastCycleDurationMS = 6400
	published.Setup.Failed = []string{"gpu"}
	published.Setup.Tasks = map[string]status.ServiceStatus{
		"gpu": {State: "FAILED", Message: "no GPU"},
	}
	published.Monitoring.CPU.UsagePercent = 35.5

	body, err := json.Marshal(published)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/status", strings.NewReader(string(body))),
	)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("post code = %d, want 204", recorder.Code)
	}

	recorder = get(t, router, "/api/v1/status")

	var got status.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Cycle != 7 || got.State != "FAILED" || got.Version != "v1.2.3" {
		t.Errorf("round trip lost data: %+v", got)
	}

	if got.Setup.LastCycleDurationMS != 6400 {
		t.Errorf("duration = %d, want 6400", got.Setup.LastCycleDurationMS)
	}

	if message := got.Setup.Tasks["gpu"].Message; message != "no GPU" {
		t.Errorf("gpu message = %q, want it preserved", message)
	}
}

// TestPostRejectsGarbage keeps a malformed body from replacing the last known
// good status with a blank document.
func TestPostRejectsGarbage(t *testing.T) {
	router := newRouter()

	good := `{"cycle":5,"state":"READY"}`

	recorder := httptest.NewRecorder()
	router.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/status", strings.NewReader(good)),
	)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("seed post code = %d, want 204", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/status", strings.NewReader("{not json")),
	)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("bad post code = %d, want 400", recorder.Code)
	}

	// The last good document must still be there.
	recorder = get(t, router, "/api/v1/status")

	var got status.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Cycle != 5 {
		t.Errorf("cycle = %d, want the last good document (5) to survive", got.Cycle)
	}
}

// TestMetaTracksTaskDuration posts a task through pending to completed with a
// fake clock advanced between the posts, so /api/v1/meta reports the 30s the
// task took rather than just its final state.
func TestMetaTracksTaskDuration(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	current := now
	keeper := internals.NewKeeperWithClock(func() time.Time { return current }, now)
	router := newRouterWithKeeper(keeper)

	post := func(body string) {
		t.Helper()

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/status", strings.NewReader(body)))

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("post code = %d, want 204", recorder.Code)
		}
	}

	post(`{"setup":{"tasks":{"gpu":{"state":"PENDING"}}}}`)
	current = now.Add(30 * time.Second)
	post(`{"setup":{"tasks":{"gpu":{"state":"COMPLETED"}}}}`)

	recorder := get(t, router, "/api/v1/meta")

	if recorder.Code != http.StatusOK {
		t.Fatalf("meta code = %d, want 200", recorder.Code)
	}

	var meta struct {
		Uptime int64 `json:"service_uptime_seconds"`
		Tasks  map[string]struct {
			State string   `json:"state"`
			Took  *float64 `json:"took_seconds"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &meta); err != nil {
		t.Fatalf("meta must be valid JSON: %v", err)
	}

	if meta.Uptime != 30 {
		t.Errorf("uptime = %d, want 30", meta.Uptime)
	}

	timing, ok := meta.Tasks["gpu"]
	if !ok {
		t.Fatal("gpu missing from meta tasks")
	}

	if timing.Took == nil {
		t.Fatal("gpu took_seconds is null, want 30")
	}

	if *timing.Took != 30 {
		t.Errorf("gpu took = %v, want 30", *timing.Took)
	}
}

// TestMetaUnknownWhenAlreadyTerminal covers a service restart mid-setup: a
// task first posted as completed finished before watching began, so took
// stays null instead of a misleading 0.
func TestMetaUnknownWhenAlreadyTerminal(t *testing.T) {
	router := newRouterWithKeeper(internals.NewKeeper())

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost, "/api/v1/status", strings.NewReader(`{"setup":{"tasks":{"docker":{"state":"COMPLETED"}}}}`),
	))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("post code = %d, want 204", recorder.Code)
	}

	recorder = get(t, router, "/api/v1/meta")

	var meta struct {
		Tasks map[string]struct {
			Took *float64 `json:"took_seconds"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &meta); err != nil {
		t.Fatalf("meta must be valid JSON: %v", err)
	}

	if took := meta.Tasks["docker"].Took; took != nil {
		t.Errorf("docker took = %v, want null", *took)
	}
}

// TestEventsSendsCurrentStateOnConnect is the behaviour a browser tab needs on
// load: without it the page stays blank until the next cycle, which on an
// interval of a minute means staring at nothing.
func TestEventsSendsCurrentStateOnConnect(t *testing.T) {
	server := httptest.NewServer(newRouter())
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/v1/status",
		"application/json",
		strings.NewReader(`{"cycle":3,"state":"READY"}`),
	)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	response.Body.Close()

	// A real server, not a recorder: the stream handler does not return, so a
	// recorder would block until the test timed out.
	stream, err := http.Get(server.URL + "/api/v1/events")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer stream.Body.Close()

	if got := stream.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("content type = %q, want text/event-stream", got)
	}

	document, err := readEvent(bufio.NewReader(stream.Body))
	if err != nil {
		t.Fatalf("read the first event: %v", err)
	}

	if document.Cycle != 3 {
		t.Errorf("cycle = %d, want 3 as soon as the client connects", document.Cycle)
	}
}

// TestEventsPushesUpdates is the live part: a client already streaming has to
// see the next report without reconnecting.
func TestEventsPushesUpdates(t *testing.T) {
	server := httptest.NewServer(newRouter())
	defer server.Close()

	// The stream is cancelled before the server is closed, so the reading
	// goroutine exits instead of pinning the connection open: defers run last
	// in, first out, and server.Close blocks on live streams.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan status.Status, 4)
	errs := make(chan error, 1)

	go func() {
		request, err := http.NewRequestWithContext(
			ctx, http.MethodGet, server.URL+"/api/v1/events", nil,
		)
		if err != nil {
			errs <- err
			return
		}

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			// A cancelled context is the expected way this goroutine ends.
			if ctx.Err() != nil {
				return
			}

			errs <- err
			return
		}
		defer response.Body.Close()

		reader := bufio.NewReader(response.Body)
		for {
			document, err := readEvent(reader)
			if err != nil {
				if ctx.Err() != nil {
					return
				}

				errs <- err
				return
			}

			events <- document
		}
	}()

	select {
	case err := <-errs:
		t.Fatalf("stream: %v", err)
	case document := <-events:
		// The connect-time document, which is the empty one.
		if document.Cycle != 0 {
			t.Fatalf("first event cycle = %d, want the empty document", document.Cycle)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event on connect")
	}

	post := func(body string) {
		t.Helper()

		response, err := http.Post(server.URL+"/api/v1/status", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		response.Body.Close()
	}

	post(`{"cycle":1,"state":"FAILED"}`)

	select {
	case document := <-events:
		if document.Cycle != 1 {
			t.Errorf("pushed cycle = %d, want 1", document.Cycle)
		}
	case err := <-errs:
		t.Fatalf("stream: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the push never reached the connected client")
	}

	post(`{"cycle":2,"state":"READY"}`)

	select {
	case document := <-events:
		if document.Cycle != 2 {
			t.Errorf("pushed cycle = %d, want 2", document.Cycle)
		}
	case err := <-errs:
		t.Fatalf("stream: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the second push never reached the connected client")
	}
}

// TestPageRenders is a smoke test: the page is the whole user interface, and a
// template that fails to execute should fail here.
func TestPageRenders(t *testing.T) {
	recorder := get(t, newRouter(), "/")

	if recorder.Code != http.StatusOK {
		t.Fatalf("page code = %d, want 200", recorder.Code)
	}

	if recorder.Body.Len() < 100 {
		t.Errorf("page is %d bytes, which is too small to be the page", recorder.Body.Len())
	}

	if !strings.Contains(recorder.Body.String(), "/api/v1/events") {
		t.Error("the page does not subscribe to the event stream")
	}

	if !strings.Contains(recorder.Body.String(), "/api/v1/meta") {
		t.Error("the page does not fetch task timings")
	}
}

// TestEventsWithoutAFlusherDoesNotPanic covers the case that used to crash. The
// handler asserted w.(http.Flusher) inline, which panics on any ResponseWriter
// that does not implement it, so a wrapping middleware would take down every
// event stream with a stack trace instead of one clear error.
func TestEventsWithoutAFlusherDoesNotPanic(t *testing.T) {
	store := &internals.StatusStore{}
	hub := internals.NewStatusHub()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)

	// plainWriter is an http.ResponseWriter with no Flush method, which is what
	// a bare wrapper hands over.
	plain := &plainWriter{header: http.Header{}}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Events panicked instead of reporting the problem: %v", recovered)
		}
	}()

	NewEventHandler(store, hub).Events(plain, request)

	if plain.code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500 for a connection that cannot stream", plain.code)
	}
}

// plainWriter is a ResponseWriter with no Flush method.
type plainWriter struct {
	header http.Header
	code   int
}

func (w *plainWriter) Header() http.Header { return w.header }

func (w *plainWriter) Write(body []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}

	return len(body), nil
}

func (w *plainWriter) WriteHeader(code int) { w.code = code }

// readEvent reads one SSE frame.
func readEvent(reader *bufio.Reader) (status.Status, error) {
	var document status.Status

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return document, err
		}

		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			return document, nil
		}

		payload, found := strings.CutPrefix(line, "data: ")
		if !found {
			continue
		}

		if err := json.Unmarshal([]byte(payload), &document); err != nil {
			return document, err
		}
	}
}
