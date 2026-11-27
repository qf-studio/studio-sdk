package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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
	// project ID → number of work item requests (list, get and patch)
	workItemRequests map[string]int
	// project ID → label query value of each work item list request
	listLabelFilters map[string][]string
	// work item ID → label IDs set by each PATCH request
	patches map[string][][]string
	// project ID → states
	states map[string][]State
	// work item ID → state ID set by each PATCH request
	statePatches map[string][]string
}

func newFakePlane(t *testing.T) *fakePlane {
	return &fakePlane{
		t:                 t,
		labels:            make(map[string][]Label),
		labelFailures:     make(map[string]int),
		items:             make(map[string][]WorkItem),
		labelListRequests: make(map[string]int),
		workItemRequests:  make(map[string]int),
		listLabelFilters:  make(map[string][]string),
		patches:           make(map[string][][]string),
		states:            make(map[string][]State),
		statePatches:      make(map[string][]string),
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
	if resource == "work-items" {
		f.workItemRequests[projectID]++
	}
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
		_ = json.NewEncoder(w).Encode(statesResponse{Results: f.states[projectID]})

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
				Labels *[]string `json:"labels"`
				State  *string   `json:"state"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				f.t.Errorf("decode patch body: %v", err)
			}
			if body.Labels != nil {
				f.items[projectID][idx].LabelIDs = *body.Labels
				f.patches[id] = append(f.patches[id], *body.Labels)
			}
			if body.State != nil {
				f.items[projectID][idx].StateID = *body.State
				f.statePatches[id] = append(f.statePatches[id], *body.State)
			}
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

// startTimeout bounds each call of Start, so a Start that does not return
// fails the test and does not hang the package.
const startTimeout = 10 * time.Second

// runPoller starts the poller and waits until done reports true (or a timeout), then stops it.
func runPoller(t *testing.T, p *Poller, done func() bool) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
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
			return waitStart(t, errCh)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	return waitStart(t, errCh)
}

// waitStart waits for Start to return after its context is done.
func waitStart(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(startTimeout):
		t.Fatal("Start did not return after its context was done")
		return nil
	}
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
		workItemRequests := fake.workItemRequests["proj-b"]
		fake.mu.Unlock()
		if len(filters) != 0 {
			t.Errorf("proj-b: list requests sent with filters %v, want none", filters)
		}
		if workItemRequests != 0 {
			t.Errorf("proj-b: work item requests = %d, want none", workItemRequests)
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

		ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
		defer cancel()
		err := p.Start(ctx)
		if ctx.Err() != nil {
			t.Fatalf("Start did not return before its timeout: %v", ctx.Err())
		}
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
			if f != "b-pilot" && f != "b-in-progress" {
				t.Errorf("proj-b list request with label filter %q, want b-pilot or b-in-progress", f)
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

func TestPollerUsesTheProjectOfTheList(t *testing.T) {
	fake := newFakePlane(t)
	fake.labels["proj-a"] = projectLabelsFor("a")
	fake.labels["proj-b"] = projectLabelsFor("b")
	// The list responses of proj-b carry no project key.
	fake.items["proj-b"] = []WorkItem{
		{ID: "wi-done", Name: "Done", LabelIDs: []string{"b-pilot", "b-done"}},
		{ID: "wi-new", Name: "New", LabelIDs: []string{"b-pilot"}},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var logs syncBuffer
	handled := &handledSet{ids: make(map[string]bool)}
	p := newTestPoller(srv.URL, []string{"proj-a", "proj-b"}, &logs, handled)

	// Wait for wi-new to get its done label and for a few more polls of proj-b.
	err := runPoller(t, p, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return handled.has("wi-new") && len(fake.patches["wi-new"]) >= 3 && len(fake.listLabelFilters["proj-b"]) >= 4
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if handled.has("wi-done") {
		t.Error("work item with the done label of the project of the list was given to the handler")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	patches := fake.patches["wi-new"]
	if len(patches) == 0 || strings.Join(patches[0], ",") != "b-pilot,b-in-progress" {
		t.Errorf("wi-new patches = %v, want first patch b-pilot,b-in-progress", patches)
	}
	if got := fake.patches["wi-done"]; len(got) != 0 {
		t.Errorf("wi-done patches = %v, want none", got)
	}
}

func TestPollerRecoversOrphansOfAProjectFoundLater(t *testing.T) {
	fake := newFakePlane(t)
	fake.labels["proj-a"] = projectLabelsFor("a")
	fake.labels["proj-b"] = projectLabelsFor("b")
	fake.labelFailures["proj-b"] = 1 // start fails
	// wi-b has the in-progress label of a run before the restart.
	fake.items["proj-b"] = []WorkItem{{ID: "wi-b", Name: "B", ProjectID: "proj-b", LabelIDs: []string{"b-pilot", "b-in-progress"}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var logs syncBuffer
	handled := &handledSet{ids: make(map[string]bool)}
	p := newTestPoller(srv.URL, []string{"proj-a", "proj-b"}, &logs, handled)

	err := runPoller(t, p, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return handled.has("wi-b") && len(fake.patches["wi-b"]) >= 4
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	// PATCHes: recovery removes in-progress, then add in-progress, remove in-progress, add done.
	patches := fake.patches["wi-b"]
	want := [][]string{
		{"b-pilot"},
		{"b-pilot", "b-in-progress"},
		{"b-pilot"},
		{"b-pilot", "b-done"},
	}
	if len(patches) != len(want) {
		t.Fatalf("wi-b patches = %v, want %v", patches, want)
	}
	for i := range want {
		if strings.Join(patches[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("wi-b patch %d labels = %v, want %v", i, patches[i], want[i])
		}
	}
}

func TestPollerWarnsOnceForEachKindOfLabelLookupFailure(t *testing.T) {
	fake := newFakePlane(t)
	fake.labels["proj-a"] = projectLabelsFor("a")
	fake.labels["proj-b"] = []Label{{ID: "b-other", Name: "bug"}}
	fake.labelFailures["proj-b"] = 2 // start + first poll fail
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var logs syncBuffer
	handled := &handledSet{ids: make(map[string]bool)}
	p := newTestPoller(srv.URL, []string{"proj-a", "proj-b"}, &logs, handled)

	// Two failed lists, then at least two lists with no pilot label.
	err := runPoller(t, p, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.labelListRequests["proj-b"] >= 4
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	out := logs.String()
	for _, tc := range []struct {
		line string
		want int
	}{
		{`level=WARN msg="Failed to list labels for project" project_id=proj-b`, 1},
		{`level=DEBUG msg="Failed to list labels for project" project_id=proj-b`, 1},
		{`level=WARN msg="Pilot label not found in project; project is skipped" project_id=proj-b label=` + LabelPilot, 1},
	} {
		if n := strings.Count(out, tc.line); n != tc.want {
			t.Errorf("count of %q = %d, want %d\n%s", tc.line, n, tc.want, out)
		}
	}
	if n := strings.Count(out, `level=DEBUG msg="Pilot label not found in project; project is skipped" project_id=proj-b`); n < 1 {
		t.Errorf("no repeat of the label not found line at Debug\n%s", out)
	}
}

// runCompletedStateCase runs one work item through a poller with the given
// options and a handler that returns a successful result with prNumber. It
// returns the state IDs set on the work item, its label patches, and the
// number of OnPRCreated calls.
func runCompletedStateCase(t *testing.T, prNumber int, opts ...PollerOption) (states []string, patches [][]string, prCreated int) {
	t.Helper()
	return runCompletedStateCaseWithDelay(t, prNumber, 0, opts...)
}

// runCompletedStateCaseWithDelay is runCompletedStateCase with a test server
// that answers the label request for the done label doneLabelDelay late. The
// done label is set at once; only the answer waits.
func runCompletedStateCaseWithDelay(t *testing.T, prNumber int, doneLabelDelay time.Duration, opts ...PollerOption) (states []string, patches [][]string, prCreated int) {
	t.Helper()
	fake := newFakePlane(t)
	fake.labels["proj-a"] = projectLabelsFor("a")
	fake.states["proj-a"] = []State{
		{ID: "a-started", Name: "In Progress", Group: StateGroupStarted},
		{ID: "a-completed", Name: "Done", Group: StateGroupCompleted},
	}
	fake.items["proj-a"] = []WorkItem{{ID: "wi-1", Name: "One", ProjectID: "proj-a", LabelIDs: []string{"a-pilot"}}}
	var handler http.Handler = fake
	if doneLabelDelay > 0 {
		handler = delayDoneLabel(fake, "a-done", doneLabelDelay)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := NewClient(srv.URL, testutil.FakePlaneAPIKey, WithClientLogger(logger))
	cfg := &Config{WorkspaceSlug: "ws", ProjectIDs: []string{"proj-a"}}

	var mu sync.Mutex
	opts = append([]PollerOption{
		WithPollerLogger(logger),
		WithOnIssue(func(_ context.Context, _ *WorkItem) (*core.IssueResult, error) {
			return &core.IssueResult{Success: true, PRNumber: prNumber, PRURL: "https://example.test/pr"}, nil
		}),
		WithOnPRCreated(func(core.PRCreatedEvent) {
			mu.Lock()
			prCreated++
			mu.Unlock()
		}),
	}, opts...)
	p := NewPoller(client, cfg, 10*time.Millisecond, opts...)

	// Wait for a signal that comes after the completed-state update in
	// processIssueAsync, so the cancel of Start does not cut the update off:
	// with a pull request OnPRCreated fires last; with none the run always
	// sends the completed-state update.
	err := runPoller(t, p, func() bool {
		if prNumber > 0 {
			mu.Lock()
			defer mu.Unlock()
			return prCreated > 0
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return countState(fake.statePatches["wi-1"], "a-completed") > 0
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), fake.statePatches["wi-1"]...), append([][]string(nil), fake.patches["wi-1"]...), prCreated
}

// delayDoneLabel passes each request to next and holds the answer to a PATCH
// that sets the label doneID for delay, so the request after it starts late.
func delayDoneLabel(next http.Handler, doneID string, delay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var patch struct {
			Labels []string `json:"labels"`
		}
		_ = json.Unmarshal(body, &patch)
		if r.Method != http.MethodPatch || !slices.Contains(patch.Labels, doneID) {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		time.Sleep(delay)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func countState(states []string, id string) int {
	n := 0
	for _, s := range states {
		if s == id {
			n++
		}
	}
	return n
}

func TestPollerCompletedStateAtMerge_PRLeavesStateAlone(t *testing.T) {
	states, patches, prCreated := runCompletedStateCase(t, 42, WithCompletedStateAtMerge())

	if n := countState(states, "a-completed"); n != 0 {
		t.Errorf("completed state requests = %d (states %v), want 0", n, states)
	}
	if n := countState(states, "a-started"); n != 1 {
		t.Errorf("started state requests = %d (states %v), want 1", n, states)
	}
	want := [][]string{
		{"a-pilot", "a-in-progress"},
		{"a-pilot"},
		{"a-pilot", "a-done"},
	}
	if len(patches) != len(want) {
		t.Fatalf("label patches = %v, want %v", patches, want)
	}
	for i := range want {
		if strings.Join(patches[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("label patch %d = %v, want %v", i, patches[i], want[i])
		}
	}
	if prCreated != 1 {
		t.Errorf("OnPRCreated calls = %d, want 1", prCreated)
	}
}

func TestPollerCompletedStateAtMerge_NoPRStillCompletes(t *testing.T) {
	states, _, prCreated := runCompletedStateCase(t, 0, WithCompletedStateAtMerge())

	if n := countState(states, "a-completed"); n != 1 {
		t.Errorf("completed state requests = %d (states %v), want 1", n, states)
	}
	if prCreated != 0 {
		t.Errorf("OnPRCreated calls = %d, want 0", prCreated)
	}
}

func TestPollerCompletedStateDefault_Unchanged(t *testing.T) {
	states, _, prCreated := runCompletedStateCase(t, 42)

	if n := countState(states, "a-completed"); n != 1 {
		t.Errorf("completed state requests = %d (states %v), want 1", n, states)
	}
	if prCreated != 1 {
		t.Errorf("OnPRCreated calls = %d, want 1", prCreated)
	}
}

// mutation pin: the helper cancels at the done label -> this test fails
func TestPollerCompletedState_SlowStateUpdateIsCounted(t *testing.T) {
	states, _, prCreated := runCompletedStateCaseWithDelay(t, 42, 100*time.Millisecond)

	if n := countState(states, "a-completed"); n != 1 {
		t.Errorf("completed state requests = %d (states %v), want 1", n, states)
	}
	if prCreated != 1 {
		t.Errorf("OnPRCreated calls = %d, want 1", prCreated)
	}
}
