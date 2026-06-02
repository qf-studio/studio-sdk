//go:build integration

package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPoller_Integration_IssueDiscovery verifies real Poller discovers issues correctly.
func TestPoller_Integration_IssueDiscovery(t *testing.T) {
	var mu sync.Mutex
	apiCalls := make(map[string]int)
	processedInServer := make(map[int]bool)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		apiCalls[r.URL.Path]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/repos/test/repo/issues":
			mu.Lock()
			var issues []*Issue
			if !processedInServer[1] {
				issues = append(issues, &Issue{
					Number:    1,
					Title:     "Test Issue 1",
					Body:      "Test body 1",
					State:     "open",
					Labels:    []Label{{Name: "pilot"}},
					CreatedAt: time.Now().Add(-2 * time.Hour),
				})
			}
			if !processedInServer[2] {
				issues = append(issues, &Issue{
					Number:    2,
					Title:     "Test Issue 2",
					Body:      "Test body 2",
					State:     "open",
					Labels:    []Label{{Name: "pilot"}},
					CreatedAt: time.Now().Add(-1 * time.Hour),
				})
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(issues)
		case r.Method == http.MethodPost && len(r.URL.Path) > 0:
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body.Labels {
				if l == LabelDone {
					// Extract issue number from path
					parts := splitPath(r.URL.Path)
					for i, p := range parts {
						if p == "issues" && i+1 < len(parts) {
							num, _ := strconv.Atoi(parts[i+1])
							if num > 0 {
								mu.Lock()
								processedInServer[num] = true
								mu.Unlock()
							}
						}
					}
				}
			}
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	var processedCount atomic.Int32
	client := NewClientWithBaseURL("test-token", server.URL)
	poller, err := NewPoller(client, "test/repo", "pilot", 100*time.Millisecond,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			processedCount.Add(1)
			return nil
		}),
		WithRetryGracePeriod(0),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = poller.Start(ctx)

	if got := processedCount.Load(); got < 1 {
		t.Errorf("expected at least 1 issue processed, got %d", got)
	}
}

func splitPath(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}
