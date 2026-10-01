package publish

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/status"
)

func TestPublisherWithStatusService(t *testing.T) {
	const statusServiceURL = "http://127.0.0.1:8081"

	// Make sure the real status-service is running before executing the test.
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Check that the service is reachable.
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		statusServiceURL+"/api/v1/status",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf(
			"status-service is not reachable at %s: %v",
			statusServiceURL,
			err,
		)
	}
	resp.Body.Close()

	// Build a real status document using the actual Collector.
	document := NewCollector().Collect(
		context.Background(),
		testOptions(),
	)

	// Publish to the real status-service.
	publisher := NewPublisher("", 8081, true)

	publisher.Publish(
		context.Background(),
		document,
	)

	// Read the document back from the real status-service.
	req, err = http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		statusServiceURL+"/api/v1/status",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("could not read status from status-service: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/status returned %s", resp.Status)
	}

	var received status.Status

	if err := json.NewDecoder(resp.Body).Decode(&received); err != nil {
		t.Fatalf("could not decode status response: %v", err)
	}

	// Verify that the document made the complete round trip.
	if received.Version != document.Version {
		t.Errorf(
			"version = %q, want %q",
			received.Version,
			document.Version,
		)
	}

	if received.Cycle != document.Cycle {
		t.Errorf(
			"cycle = %d, want %d",
			received.Cycle,
			document.Cycle,
		)
	}

	if received.State != document.State {
		t.Errorf(
			"state = %q, want %q",
			received.State,
			document.State,
		)
	}

	if _, ok := received.Setup.Tasks[setup.TaskDocker]; !ok {
		t.Errorf(
			"docker task missing from received status: %v",
			received.Setup.Tasks,
		)
	}
}
