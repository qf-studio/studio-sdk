# Webhooks Example

Receive a GitHub webhook, verify the HMAC signature, and react to `issues.opened`.

```go
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	handler := github.NewWebhookHandler(github.WebhookConfig{
		Secret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		Logger: logger,
	})

	// React to issues.opened events.
	handler.On("issues.opened", func(evt github.WebhookEvent) error {
		issue := evt.Issue
		fmt.Printf("new issue #%d: %s\n", issue.Number, issue.Title)
		return nil
	})

	http.Handle("/webhook", handler)
	logger.Info("listening", "addr", ":8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
```

**What this does:**

1. Creates a `WebhookHandler` that verifies the `X-Hub-Signature-256` header
   against `GITHUB_WEBHOOK_SECRET` on every incoming request.
2. Registers a callback for the `issues.opened` event action.
3. Starts an HTTP server on `:8080`; point the GitHub webhook URL at
   `https://<host>/webhook`.

Requests with an invalid or missing signature are rejected with `401` before
any callback fires.
