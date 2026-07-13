package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func newTestSyncClient(t *testing.T, handler http.HandlerFunc) *SyncClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	return NewSyncClient(client, "owner", "repo")
}

func TestSyncClient_ImplementsSyncCapable(t *testing.T) {
	var _ core.SyncCapable = (*SyncClient)(nil)
}

// TestListUpdatedSince_MultiPage verifies exhaustive cursor pagination and
// that the since filter, sort, and direction params reach the API.
func TestSyncClient_ListUpdatedSince_MultiPage(t *testing.T) {
	page1 := make([]*Issue, issuesSyncPerPage)
	for i := range page1 {
		page1[i] = &Issue{Number: i + 1, Title: fmt.Sprintf("Issue %d", i+1), State: "open"}
	}
	page2 := []*Issue{
		{Number: 101, Title: "Issue 101", State: "closed"},
		{Number: 102, Title: "Issue 102", State: "open"},
	}

	var gotSince, gotSort, gotDirection []string
	calls := 0
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		gotSince = append(gotSince, q.Get("since"))
		gotSort = append(gotSort, q.Get("sort"))
		gotDirection = append(gotDirection, q.Get("direction"))
		if q.Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", q.Get("per_page"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if q.Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(page2)
		} else {
			_ = json.NewEncoder(w).Encode(page1)
		}
	})

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps1, cursor1, err := client.ListUpdatedSince(context.Background(), "", since, "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(snaps1) != issuesSyncPerPage {
		t.Fatalf("page 1: got %d snapshots, want %d", len(snaps1), issuesSyncPerPage)
	}
	if cursor1 == "" {
		t.Fatalf("page 1: expected non-empty next cursor")
	}

	snaps2, cursor2, err := client.ListUpdatedSince(context.Background(), "", since, cursor1)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(snaps2) != 2 {
		t.Fatalf("page 2: got %d snapshots, want 2", len(snaps2))
	}
	if cursor2 != "" {
		t.Fatalf("page 2: expected empty (exhausted) cursor, got %q", cursor2)
	}

	if calls != 2 {
		t.Fatalf("expected 2 API calls, got %d", calls)
	}
	for _, v := range gotSince {
		if v != since.Format(time.RFC3339) {
			t.Errorf("since = %q, want %q", v, since.Format(time.RFC3339))
		}
	}
	for _, v := range gotSort {
		if v != "updated" {
			t.Errorf("sort = %q, want %q", v, "updated")
		}
	}
	for _, v := range gotDirection {
		if v != "asc" {
			t.Errorf("direction = %q, want %q", v, "asc")
		}
	}
}

// TestListUpdatedSince_FiltersPullRequests verifies that items carrying a
// pull_request key are excluded from the snapshot list, and that a page
// consisting only of PRs still correctly reports exhaustion via the raw
// batch length (not the post-filter count).
func TestSyncClient_ListUpdatedSince_FiltersPullRequests(t *testing.T) {
	batch := []*Issue{
		{Number: 1, Title: "Real issue"},
		{Number: 2, Title: "A PR", PullRequest: &struct{}{}},
		{Number: 3, Title: "Another real issue"},
	}

	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(batch)
	})

	snaps, cursor, err := client.ListUpdatedSince(context.Background(), "", time.Time{}, "")
	if err != nil {
		t.Fatalf("ListUpdatedSince() error = %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2 (PR excluded)", len(snaps))
	}
	for _, s := range snaps {
		if s.NativeID == "2" {
			t.Errorf("PR #2 should have been filtered out")
		}
	}
	if cursor != "" {
		t.Errorf("cursor = %q, want empty (batch < per_page)", cursor)
	}
}

func TestSyncClient_ListAll_ExhaustsPages(t *testing.T) {
	page1 := make([]*Issue, issuesSyncPerPage)
	for i := range page1 {
		page1[i] = &Issue{Number: i + 1}
	}
	page2 := []*Issue{{Number: 101}}

	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("since") != "" {
			t.Errorf("ListAll should not set since, got %q", q.Get("since"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if q.Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(page2)
		} else {
			_ = json.NewEncoder(w).Encode(page1)
		}
	})

	var all []core.IssueSnapshot
	page := core.Cursor("")
	for {
		snaps, next, err := client.ListAll(context.Background(), "", page)
		if err != nil {
			t.Fatalf("ListAll() error = %v", err)
		}
		all = append(all, snaps...)
		if next == "" {
			break
		}
		page = next
	}

	if len(all) != issuesSyncPerPage+1 {
		t.Fatalf("got %d snapshots, want %d", len(all), issuesSyncPerPage+1)
	}
}

func TestSyncClient_GetIssue(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/issues/42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(&Issue{
			Number:  42,
			Title:   "Test issue",
			Body:    "body",
			State:   "open",
			Labels:  []Label{{Name: "priority:high"}},
			HTMLURL: "https://github.com/owner/repo/issues/42",
		})
	})

	snap, err := client.GetIssue(context.Background(), "42")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}
	if snap.NativeID != "42" {
		t.Errorf("NativeID = %q, want %q", snap.NativeID, "42")
	}
	if snap.SequenceID != "GH-42" {
		t.Errorf("SequenceID = %q, want %q", snap.SequenceID, "GH-42")
	}
	if snap.State != "open" || snap.StateGroup != "open" {
		t.Errorf("State/StateGroup = %q/%q, want open/open", snap.State, snap.StateGroup)
	}
	if snap.Priority != core.PriorityHigh {
		t.Errorf("Priority = %q, want %q", snap.Priority, core.PriorityHigh)
	}
}

func TestSyncClient_GetIssue_RejectsPullRequest(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(&Issue{Number: 7, PullRequest: &struct{}{}})
	})

	if _, err := client.GetIssue(context.Background(), "7"); err == nil {
		t.Fatal("expected error for nativeID referring to a pull request")
	}
}

func TestSyncClient_UpdateFields_PartialPatch(t *testing.T) {
	var gotBody map[string]interface{}
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if r.URL.Path != "/repos/owner/repo/issues/9" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(&Issue{Number: 9, Title: "Updated title", State: "open"})
	})

	snap, err := client.UpdateFields(context.Background(), "9", core.FieldPatch{
		"title": "Updated title",
	})
	if err != nil {
		t.Fatalf("UpdateFields() error = %v", err)
	}
	if _, hasBody := gotBody["body"]; hasBody {
		t.Errorf("partial patch should not send unset field %q", "body")
	}
	if _, hasLabels := gotBody["labels"]; hasLabels {
		t.Errorf("partial patch should not send unset field %q", "labels")
	}
	if gotBody["title"] != "Updated title" {
		t.Errorf("title = %v, want %q", gotBody["title"], "Updated title")
	}
	if snap.Title != "Updated title" {
		t.Errorf("snapshot Title = %q, want %q", snap.Title, "Updated title")
	}
}

func TestSyncClient_UpdateFields_InvalidType(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("API should not be called for an invalid patch")
	})

	if _, err := client.UpdateFields(context.Background(), "9", core.FieldPatch{
		"title": 123, // wrong type
	}); err == nil {
		t.Fatal("expected error for non-string title field")
	}
}

func TestSyncClient_TransitionState(t *testing.T) {
	var gotBody map[string]string
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})

	if err := client.TransitionState(context.Background(), "5", "closed"); err != nil {
		t.Fatalf("TransitionState() error = %v", err)
	}
	if gotBody["state"] != "closed" {
		t.Errorf("state = %q, want %q", gotBody["state"], "closed")
	}
}

// TestAddComment_Idempotent verifies that a retry which finds the idempotency
// marker already present on an existing comment does not repost.
func TestSyncClient_AddComment_Idempotent(t *testing.T) {
	posts := 0
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/3/comments":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*Comment{
				{ID: 1, Body: "unrelated comment"},
				{ID: 2, Body: "synced\n\n<!-- pilot-op:key-123 -->"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/3/comments":
			posts++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(&Comment{ID: 3})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.AddComment(context.Background(), "3", "synced", "key-123"); err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	if posts != 0 {
		t.Errorf("posts = %d, want 0 (marker already present)", posts)
	}
}

func TestSyncClient_AddComment_PostsWhenMarkerAbsent(t *testing.T) {
	posts := 0
	var postedBody string
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/3/comments":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*Comment{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/3/comments":
			posts++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			postedBody = body["body"]
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(&Comment{ID: 3})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := client.AddComment(context.Background(), "3", "synced", "key-123"); err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	if posts != 1 {
		t.Fatalf("posts = %d, want 1", posts)
	}
	if !strings.Contains(postedBody, "<!-- pilot-op:key-123 -->") {
		t.Errorf("posted body missing idempotency marker: %q", postedBody)
	}
}

func TestSyncClient_CreateIssue(t *testing.T) {
	var gotBody IssueInput
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/issues" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(&Issue{
			Number: 55,
			Title:  gotBody.Title,
			Body:   gotBody.Body,
			State:  "open",
		})
	})

	snap, err := client.CreateIssue(context.Background(), "", core.IssueDraft{
		Title:  "New issue",
		Body:   "Description",
		Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if snap.SequenceID != "GH-55" {
		t.Errorf("SequenceID = %q, want %q", snap.SequenceID, "GH-55")
	}
	if gotBody.Title != "New issue" {
		t.Errorf("posted title = %q, want %q", gotBody.Title, "New issue")
	}
}

func TestSyncClient_CreateIssue_ProjectIDOverridesBoundRepo(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/other-owner/other-repo/issues" {
			t.Errorf("unexpected path: %s, want repo from projectID", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(&Issue{Number: 1})
	})

	if _, err := client.CreateIssue(context.Background(), "other-owner/other-repo", core.IssueDraft{Title: "x"}); err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
}
