package handlers

import (
	"log"
	"net/http"
)

func Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	page := `
<!DOCTYPE html>
<html>
<head>
	<title>Chotu Status</title>
</head>
<body>
	<h1>Chotu Status</h1>

	<pre id="status">Waiting for status...</pre>

	<script>
		const status = document.getElementById("status");

		const events = new EventSource("/api/v1/events");

		events.onmessage = function(event) {
			const data = JSON.parse(event.data);
			status.textContent = JSON.stringify(data, null, 2);
		};

		events.onerror = function() {
			status.textContent = "Connection lost";
		};
	</script>
</body>
</html>
`

	// A write failure here means the browser went away before the page finished
	// arriving, which is not worth failing over.
	if _, err := w.Write([]byte(page)); err != nil {
		log.Printf("[page] could not write the page: %v", err)
	}
}
