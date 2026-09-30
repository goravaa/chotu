package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/goravaa/chotu/status"
)

// Publisher sends status documents to the status service.
type Publisher struct {
	endpoint string
	port     int
	client   *http.Client

	// enabled is false when the status page is switched off in the config. The
	// daemon then does no work at all rather than retrying a service that will
	// never appear.
	enabled bool
}

func NewPublisher(_ string, port int, enabled bool) *Publisher {
	if !enabled {
		return &Publisher{enabled: false}
	}

	if port == 0 {
		port = 8081
	}

	return &Publisher{
		enabled:  true,
		port:     port,
		endpoint: fmt.Sprintf("http://127.0.0.1:%d/api/v1/status", port),
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				// The status service is on the same machine, so a fresh
				// connection per document is cheaper than holding a pool open
				// for a service that may not be running yet.
				DialContext: (&net.Dialer{
					Timeout: 3 * time.Second,
				}).DialContext,
				DisableKeepAlives: true,
			},
		},
	}
}

func (p *Publisher) Enabled() bool { return p != nil && p.enabled }

// Publish sends one document. It never returns an error to the caller: status
// reporting is a side effect of setup, and a status service that is down must
// not turn a working machine into a failing one. Failures are logged, and the
// next cycle tries again.
func (p *Publisher) Publish(ctx context.Context, document status.Status) {
	if !p.Enabled() {
		return
	}

	body, err := json.Marshal(document)
	if err != nil {
		log.Printf("[status] could not encode the status document: %v", err)
		return
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		log.Printf("[status] could not build the request: %v", err)
		return
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := p.client.Do(request)
	if err != nil {
		log.Printf("[status] could not reach the status service on port %d: %v", p.port, err)
		return
	}

	defer response.Body.Close()
	// The body has to be drained for the connection to be reusable, though this
	// transport disables keep-alives, so reading and discarding is enough.
	_, _ = io.Copy(io.Discard, response.Body)

	if response.StatusCode != http.StatusNoContent {
		log.Printf(
			"[status] the status service rejected the document: %s",
			response.Status,
		)
	}
}
