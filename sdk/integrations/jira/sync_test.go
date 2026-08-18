package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(jiraDateLayout, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm
}

func jsonNumberString(v interface{}) string {
	f, ok := v.(float64)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return strconv.Itoa(int(f))
}

func newTestSyncClient(t *testing.T, handler http.HandlerFunc) *SyncClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	client.retryOpts = RetryOptions{MaxRetries: 0}
	return NewSyncClient(client, "PROJ")
}

func TestSyncClient_ImplementsSyncCapable(t *testing.T) {
	var _ core.SyncCapable = (*SyncClient)(nil)
}

func testIssue(key, statusName, categoryKey string) *Issue {
	return &Issue{
		ID:  "1" + key,
		Key: key,
		Fields: Fields{
			Summary:     "Summary " + key,
			Description: ADFText("Description " + key),
			Status: Status{
				Name:           statusName,
				StatusCategory: StatusCategory{Key: categoryKey, Name: statusName},
			},
			Labels:  []string{"bug"},
			Created: "2026-01-01T10:00:00.000+0000",
			Updated: "2026-01-02T10:00:00.000+0000",
		},
	}
}

// TestSyncClient_ListUpdatedSince_MultiPage verifies exhaustive startAt-based
// pagination and that the JQL project filter + updated-since clause reach the
// API.
func TestSyncClient_ListUpdatedSince_MultiPage(t *testing.T) {
	page1 := make([]*Issue, jiraSyncPerPage)
	for i := range page1 {
		page1[i] = testIssue(fmt.Sprintf("PROJ-%d", i+1), "To Do", "new")
	}
	page2 := []*Issue{testIssue("PROJ-101", "Done", "done")}

	var gotJQL []string
	var gotStartAt []string
	calls := 0
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotJQL = append(gotJQL, body["jql"].(string))
		gotStartAt = append(gotStartAt, jsonNumberString(body["startAt"]))

		w.Header().Set("Content-Type", "application/json")
		if body["startAt"].(float64) == 0 {
			_ = json.NewEncoder(w).Encode(SearchResponse{Issues: page1})
		} else {
			_ = json.NewEncoder(w).Encode(SearchResponse{Issues: page2})
		}
	})

	since := mustParseTime(t, "2026-01-01T00:00:00.000+0000")
	snaps1, cursor1, err := client.ListUpdatedSince(context.Background(), "", since, "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(snaps1) != jiraSyncPerPage {
		t.Fatalf("page 1: got %d snapshots, want %d", len(snaps1), jiraSyncPerPage)
	}
	if cursor1 == "" {
		t.Fatal("page 1: expected non-empty next cursor")
	}

	snaps2, cursor2, err := client.ListUpdatedSince(context.Background(), "", since, cursor1)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(snaps2) != 1 {
		t.Fatalf("page 2: got %d snapshots, want 1", len(snaps2))
	}
	if cursor2 != "" {
		t.Fatalf("page 2: expected empty (exhausted) cursor, got %q", cursor2)
	}
	if calls != 2 {
		t.Fatalf("expected 2 API calls, got %d", calls)
	}
	for _, jql := range gotJQL {
		if !contains(jql, "project = PROJ") || !contains(jql, "updated >=") {
			t.Errorf("jql = %q, missing project/updated filter", jql)
		}
	}
	if gotStartAt[0] != "0" || gotStartAt[1] != "50" {
		t.Errorf("startAt sequence = %v, want [0 50]", gotStartAt)
	}
}

func TestSyncClient_ListAll_NoSinceFilter(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if contains(body["jql"].(string), "updated >=") {
			t.Errorf("ListAll should not filter by updated, got jql %q", body["jql"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SearchResponse{Issues: []*Issue{testIssue("PROJ-1", "To Do", "new")}})
	})

	snaps, cursor, err := client.ListAll(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snaps))
	}
	if cursor != "" {
		t.Errorf("cursor = %q, want empty (batch < per_page)", cursor)
	}
}

func TestSyncClient_StateGroupMapping(t *testing.T) {
	tests := []struct {
		categoryKey  string
		categoryName string
		want         string
	}{
		{"new", "To Do", "To Do"},
		{"indeterminate", "In Progress", "In Progress"},
		{"done", "Done", "Done"},
		{"custom", "Custom Status", "Custom Status"},
	}
	for _, tt := range tests {
		t.Run(tt.categoryKey, func(t *testing.T) {
			client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(testIssue("PROJ-1", "Custom Status", tt.categoryKey))
			})
			snap, err := client.GetIssue(context.Background(), "PROJ-1")
			if err != nil {
				t.Fatalf("GetIssue() error = %v", err)
			}
			if snap.StateGroup != tt.want {
				t.Errorf("StateGroup = %q, want %q", snap.StateGroup, tt.want)
			}
		})
	}
}

func TestSyncClient_GetIssue_Fields(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/PROJ-42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testIssue("PROJ-42", "In Progress", "indeterminate"))
	})

	snap, err := client.GetIssue(context.Background(), "PROJ-42")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}
	if snap.NativeID != "PROJ-42" || snap.SequenceID != "PROJ-42" {
		t.Errorf("NativeID/SequenceID = %q/%q, want PROJ-42/PROJ-42", snap.NativeID, snap.SequenceID)
	}
	if snap.State != "In Progress" || snap.StateGroup != "In Progress" {
		t.Errorf("State/StateGroup = %q/%q, want In Progress/In Progress", snap.State, snap.StateGroup)
	}
}

func TestSyncClient_UpdateFields_PartialPatch(t *testing.T) {
	var putBody map[string]interface{}
	calls := 0
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.Method {
		case http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Fatalf("decode: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(testIssue("PROJ-42", "To Do", "new"))
		}
	})

	snap, err := client.UpdateFields(context.Background(), "PROJ-42", core.FieldPatch{
		"title": "Updated title",
	})
	if err != nil {
		t.Fatalf("UpdateFields() error = %v", err)
	}
	fields := putBody["fields"].(map[string]interface{})
	if fields["summary"] != "Updated title" {
		t.Errorf("summary = %v, want %q", fields["summary"], "Updated title")
	}
	if _, hasDescription := fields["description"]; hasDescription {
		t.Error("partial patch should not send unset field description")
	}
	if snap.NativeID != "PROJ-42" {
		t.Errorf("snapshot NativeID = %q, want PROJ-42", snap.NativeID)
	}
}

func TestSyncClient_UpdateFields_InvalidType(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("API should not be called for an invalid patch")
	})

	if _, err := client.UpdateFields(context.Background(), "PROJ-42", core.FieldPatch{
		"title": 123,
	}); err == nil {
		t.Fatal("expected error for non-string title field")
	}
}

func TestSyncClient_TransitionState(t *testing.T) {
	var transitioned string
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && contains(r.URL.Path, "/transitions"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TransitionsResponse{
				Transitions: []Transition{{ID: "31", Name: "Done", To: Status{Name: "Done"}}},
			})
		case r.Method == http.MethodPost && contains(r.URL.Path, "/transitions"):
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			transitioned = body["transition"].(map[string]interface{})["id"].(string)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	if err := client.TransitionState(context.Background(), "PROJ-42", "Done"); err != nil {
		t.Fatalf("TransitionState() error = %v", err)
	}
	if transitioned != "31" {
		t.Errorf("transitioned to id %q, want 31", transitioned)
	}
}

func TestSyncClient_AddComment_Idempotent(t *testing.T) {
	posts := 0
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"comments":[{"id":"1","body":"synced\n\n<!-- pilot-op:key-123 -->"}]}`))
		case http.MethodPost:
			posts++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "2"})
		}
	})

	if err := client.AddComment(context.Background(), "PROJ-42", "synced", "key-123"); err != nil {
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
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"comments":[]}`))
		case http.MethodPost:
			posts++
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodyContent := body["body"].(map[string]interface{})
			text := bodyContent["content"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
			postedBody = text
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "2"})
		}
	})

	if err := client.AddComment(context.Background(), "PROJ-42", "synced", "key-123"); err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	if posts != 1 {
		t.Fatalf("posts = %d, want 1", posts)
	}
	if !contains(postedBody, "<!-- pilot-op:key-123 -->") {
		t.Errorf("posted body missing idempotency marker: %q", postedBody)
	}
}

func TestSyncClient_CreateIssue(t *testing.T) {
	client := newTestSyncClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(&IssueCreateResponse{ID: "155", Key: "PROJ-55"})
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(testIssue("PROJ-55", "To Do", "new"))
		}
	})

	snap, err := client.CreateIssue(context.Background(), "", core.IssueDraft{
		Title:  "New issue",
		Body:   "Description",
		Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if snap.SequenceID != "PROJ-55" {
		t.Errorf("SequenceID = %q, want PROJ-55", snap.SequenceID)
	}
}
