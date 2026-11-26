package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// fakePlane is a fake Plane API for poller tests. Each project has its own
// labels and work items; PATCH requests update the labels of a work item.
type fakePlane struct {
	t *testing.T

	mu sync.Mutex
	// project ID → labels
	labels map[string][]Label
	// project ID → number of label list requests left that fail with 500
	labelFailures map[string]int
	// project ID → work items
	items map[string][]WorkItem

	labelListRequests map[string]int
	// project ID → label query value of each work item list request
	listLabelFilters map[string][]string
	// work item ID → label IDs set by each PATCH request
	patches map[string][][]string
}

func newFakePlane(t *testing.T) *fakePlane {
	return &fakePlane{
		t:                 t,
		labels:            make(map[string][]Label),
		labelFailures:     make(map[string]int),
		items:             make(map[string][]WorkItem),
		labelListRequests: make(map[string]int),
		listLabelFilters:  make(map[string][]string),
		patches:           make(map[string][][]string),
	}
}

func (f *fakePlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != testutil.FakePlaneAPIKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// /api/v1/workspaces/{ws}/projects/{project}/{resource}/[{id}/]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 || parts[0] != "api" || parts[2] != "workspaces" || parts[4] != "projects" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	projectID := parts[5]
	resource := ""
	if len(parts) > 6 {
		resource = parts[6]
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case resource == "labels":
		f.labelListRequests[projectID]++
		if f.labelFailures[projectID] > 0 {
			f.labelFailures[projectID]--
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(labelsResponse{Results: f.labels[projectID]})

	case resource == "states":
		_ = json.NewEncoder(w).Encode(statesResponse{})

	case resource == "work-items" && len(parts) == 7 && r.Method == http.MethodGet:
		label := r.URL.Query().Get("label")
		f.listLabelFilters[projectID] = append(f.listLabelFilters[projectID], label)
		var out []WorkItem
		for _, it := range f.items[projectID] {
			if label == "" || HasLabelID(&it, label) {
				out = append(out, it)
			}
		}
		_ = json.NewEncoder(w).Encode(paginatedResponse{Results: out, TotalCount: len(out)})

	case resource == "work-items" && len(parts) == 8:
		id := parts[7]
		idx := -1
		for i, it := range f.items[projectID] {
			if it.ID == id {
				idx = i
			}
		}
		if idx < 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.items[projectID][idx])
		case http.MethodPatch:
			var body struct {
				Labels []string `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				f.t.Errorf("decode patch body: %v", err)
			}
			f.items[projectID][idx].LabelIDs = body.Labels
			f.patches[id] = append(f.patches[id], body.Labels)
			_ = json.NewEncoder(w).Encode(f.items[projectID][idx])
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func projectLabelsFor(prefix string) []Label {
	return []Label{
		{ID: prefix + "-pilot", Name: LabelPilot},
		{ID: prefix + "-in-progress", Name: LabelInProgress},
		{ID: prefix + "-done", Name: LabelDone},
		{ID: prefix + "-failed", Name: LabelFailed},
	}
}

// runPoller starts the poller and waits until done reports true (or a timeout), then stops it.
func runPoller(t *testing.T, p *Poller, done func() bool) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- p.Start(ctx) }()

	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case err := <-errCh:
			cancel()
			return err
		case <-deadline:
			// Stop and let the caller's checks report what is missing.
			t.Error("timed out waiting for the poller")
			cancel()
			return <-errCh
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	return <-errCh
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type handledSet struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (h *handledSet) add(id string) {
	h.mu.Lock()
	h.ids[id] = true
	h.mu.Unlock()
}

func (h *handledSet) has(ids ...string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		if !h.ids[id] {
			return false
		}
	}
	return true
}

func newTestPoller(srvURL string, projectIDs []string, logs *syncBuffer, handled *handledSet) *Poller {
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := NewClient(srvURL, testutil.FakePlaneAPIKey, WithClientLogger(logger))
	cfg := &Config{WorkspaceSlug: "ws", ProjectIDs: projectIDs}
	return NewPoller(client, cfg, 10*time.Millisecond,
		WithPollerLogger(logger),
		WithOnIssue(func(_ context.Context, item *WorkItem) (*core.IssueResult, error) {
			handled.add(item.ID)
			return &core.IssueResult{Success: true}, nil
		}),
	)
}

func TestPollerLooksUpLabelsForEachProject(t *testing.T) {
	t.Run("two projects with their own label IDs", func(t *testing.T) {
		fake := newFakePlane(t)
		fake.labels["proj-a"] = projectLabelsFor("a")
		fake.labels["proj-b"] = projectLabelsFor("b")
		fake.items["proj-a"] = []WorkItem{{ID: "wi-a", Name: "A", ProjectID: "proj-a", LabelIDs: []string{"a-pilot"}}}
		fake.items["proj-b"] = []WorkItem{{ID: "wi-b", Name: "B", ProjectID: "proj-b", LabelIDs: []string{"b-pilot"}}}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		var logs syncBuffer
		handled := &handledSet{ids: make(map[string]bool)}
		p := newTestPoller(srv.URL, []string{"proj-a", "proj-b"}, &logs, handled)

		// Wait until both work items are handled and got their done label.
		err := runPoller(t, p, func() bool {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return handled.has("wi-a", "wi-b") && len(fake.patches["wi-a"]) >= 3 && len(fake.patches["wi-b"]) >= 3
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}

		fake.mu.Lock()
		defer fake.mu.Unlock()

		for _, tc := range []struct{ project, prefix, item string }{
			{"proj-a", "a", "wi-a"},
			{"proj-b", "b", "wi-b"},
		} {
			if fake.labelListRequests[tc.project] != 1 {
				t.Errorf("%s: label list requests = %d, want 1", tc.project, fake.labelListRequests[tc.project])
			}

			seen := make(map[string]bool)
			for _, f := range fake.listLabelFilters[tc.project] {
				seen[f] = true
			}
			for f := range seen {
				if f != tc.prefix+"-pilot" && f != tc.prefix+"-in-progress" {
					t.Errorf("%s: list request with label filter %q, want only IDs of this project", tc.project, f)
				}
			}
			if !seen[tc.prefix+"-pilot"] {
				t.Errorf("%s: no list request with pilot label %q", tc.project, tc.prefix+"-pilot")
			}

			// PATCHes: add in-progress, remove in-progress, add done.
			patches := fake.patches[tc.item]
			want := [][]string{
				{tc.prefix + "-pilot", tc.prefix + "-in-progress"},
				{tc.prefix + "-pilot"},
				{tc.prefix + "-pilot", tc.prefix + "-done"},
			}
			if len(patches) != len(want) {
				t.Fatalf("%s: patches = %v, want %v", tc.item, patches, want)
			}
			for i := range want {
				if strings.Join(patches[i], ",") != strings.Join(want[i], ",") {
					t.Errorf("%s: patch %d labels = %v, want %v", tc.item, i, patches[i], want[i])
				}
			}
		}
	})

	t.Run("project with no pilot label is skipped", func(t *testing.T) {
		fake := newFakePlane(t)
		fake.labels["proj-a"] = projectLabelsFor("a")
		fake.labels["proj-b"] = []Label{{ID: "b-other", Name: "bug"}}
		fake.items["proj-a"] = []WorkItem{{ID: "wi-a", Name: "A", ProjectID: "proj-a", LabelIDs: []string{"a-pilot"}}}
		fake.items["proj-b"] = []WorkItem{{ID: "wi-b", Name: "B", ProjectID: "proj-b"}}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		var logs syncBuffer
		handled := &handledSet{ids: make(map[string]bool)}
		p := newTestPoller(srv.URL, []string{"proj-a", "proj-b"}, &logs, handled)

		// Wait for wi-a and a few polls that look up proj-b again.
		err := runPoller(t, p, func() bool {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return handled.has("wi-a") && fake.labelListRequests["proj-b"] >= 3
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}

		if handled.has("wi-b") {
			t.Error("work item of the project without the pilot label was handled")
		}

		fake.mu.Lock()
		filters := fake.listLabelFilters["proj-b"]
		fake.mu.Unlock()
		if len(filters) != 0 {
			t.Errorf("proj-b: list requests sent with filters %v, want none", filters)
		}

		var warns int
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "project_id=proj-b") {
				warns++
				if !strings.Contains(line, "label="+LabelPilot) {
					t.Errorf("warn does not name the label: %s", line)
				}
			}
		}
		if warns != 1 {
			t.Errorf("proj-b warns = %d, want 1\n%s", warns, logs.String())
		}
	})

	t.Run("no project has the pilot label", func(t *testing.T) {
		fake := newFakePlane(t)
		fake.labels["proj-a"] = []Label{{ID: "a-other", Name: "bug"}}
		fake.labels["proj-b"] = []Label{{ID: "b-other", Name: "bug"}}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		var logs syncBuffer
		handled := &handledSet{ids: make(map[string]bool)}
		p := newTestPoller(srv.URL, []string{"proj-a", "proj-b"}, &logs, handled)

		err := p.Start(context.Background())
		if err == nil {
			t.Fatal("Start returned nil, want error")
		}
		if !strings.Contains(err.Error(), `pilot label "pilot" not found in any configured project`) {
			t.Errorf("Start error = %v", err)
		}
	})

	t.Run("project whose label list fails at start is picked up later", func(t *testing.T) {
		fake := newFakePlane(t)
		fake.labels["proj-a"] = projectLabelsFor("a")
		fake.labels["proj-b"] = projectLabelsFor("b")
		fake.labelFailures["proj-b"] = 2 // start + first poll fail
		fake.items["proj-a"] = []WorkItem{{ID: "wi-a", Name: "A", ProjectID: "proj-a", LabelIDs: []string{"a-pilot"}}}
		fake.items["proj-b"] = []WorkItem{{ID: "wi-b", Name: "B", ProjectID: "proj-b", LabelIDs: []string{"b-pilot"}}}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		var logs syncBuffer
		handled := &handledSet{ids: make(map[string]bool)}
		p := newTestPoller(srv.URL, []string{"proj-a", "proj-b"}, &logs, handled)

		err := runPoller(t, p, func() bool {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return handled.has("wi-a", "wi-b") && len(fake.patches["wi-b"]) >= 3
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}

		fake.mu.Lock()
		defer fake.mu.Unlock()
		if got := fake.labelListRequests["proj-b"]; got != 3 {
			t.Errorf("proj-b label list requests = %d, want 3", got)
		}
		for _, f := range fake.listLabelFilters["proj-b"] {
			if f != "b-pilot" {
				t.Errorf("proj-b list request with label filter %q, want b-pilot", f)
			}
		}
		if got := fake.patches["wi-b"]; len(got) != 3 || strings.Join(got[2], ",") != "b-pilot,b-done" {
			t.Errorf("wi-b patches = %v, want done label b-done", got)
		}

		out := logs.String()
		if n := strings.Count(out, `level=WARN msg="Failed to list labels for project" project_id=proj-b`); n != 1 {
			t.Errorf("proj-b failure warns = %d, want 1\n%s", n, out)
		}
		if n := strings.Count(out, `level=DEBUG msg="Failed to list labels for project" project_id=proj-b`); n != 1 {
			t.Errorf("proj-b failure debug lines = %d, want 1\n%s", n, out)
		}
		if n := strings.Count(out, `level=INFO msg="Found pilot label for project; project is now polled" project_id=proj-b`); n != 1 {
			t.Errorf("proj-b pickup info lines = %d, want 1\n%s", n, out)
		}
	})
}
