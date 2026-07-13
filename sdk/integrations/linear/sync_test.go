package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// TestSyncAdapterConformance is a compile-time + runtime check that
// *SyncAdapter satisfies core.SyncCapable (core.SyncSource + core.SyncWriter).
func TestSyncAdapterConformance(t *testing.T) {
	var _ core.SyncCapable = (*SyncAdapter)(nil)
}

func TestToSnapshot_StateGroupTaxonomy(t *testing.T) {
	tests := []struct {
		name         string
		stateType    string
		wantStateGrp string
	}{
		{"backlog", "backlog", "backlog"},
		{"unstarted", "unstarted", "unstarted"},
		{"started", "started", "started"},
		{"completed", "completed", "completed"},
		{"canceled", "canceled", "canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				ID:         "issue-1",
				Identifier: "ENG-42",
				Title:      "Test issue",
				Priority:   2,
				State:      State{ID: "state-1", Name: "In Review", Type: tt.stateType},
			}

			snap := toSnapshot(issue)

			if snap.SequenceID != "ENG-42" {
				t.Errorf("SequenceID = %q, want ENG-42", snap.SequenceID)
			}
			if snap.State != "In Review" {
				t.Errorf("State = %q, want %q", snap.State, "In Review")
			}
			if snap.StateGroup != tt.wantStateGrp {
				t.Errorf("StateGroup = %q, want %q", snap.StateGroup, tt.wantStateGrp)
			}
			if snap.Priority != core.PriorityHigh {
				t.Errorf("Priority = %q, want %q", snap.Priority, core.PriorityHigh)
			}
		})
	}
}

func TestToSnapshot_Fields(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	issue := &Issue{
		ID:          "issue-1",
		Identifier:  "ENG-7",
		Title:       "Title",
		Description: "Body text",
		Priority:    1,
		State:       State{ID: "s1", Name: "Todo", Type: "unstarted"},
		Labels:      []Label{{ID: "l1", Name: "bug"}, {ID: "l2", Name: "urgent"}},
		Assignee:    &User{ID: "u1", Name: "Ada"},
		URL:         "https://linear.app/team/issue/ENG-7",
		CreatedAt:   created,
		UpdatedAt:   updated,
	}

	snap := toSnapshot(issue)

	if snap.NativeID != "issue-1" {
		t.Errorf("NativeID = %q, want issue-1", snap.NativeID)
	}
	if snap.Assignee != "Ada" {
		t.Errorf("Assignee = %q, want Ada", snap.Assignee)
	}
	if snap.URL != issue.URL {
		t.Errorf("URL = %q, want %q", snap.URL, issue.URL)
	}
	if len(snap.Labels) != 2 || snap.Labels[0] != "bug" || snap.Labels[1] != "urgent" {
		t.Errorf("Labels = %v, want [bug urgent]", snap.Labels)
	}
	if snap.Priority != core.PriorityUrgent {
		t.Errorf("Priority = %q, want %q", snap.Priority, core.PriorityUrgent)
	}
	if !snap.CreatedAt.Equal(created) || !snap.UpdatedAt.Equal(updated) {
		t.Errorf("timestamps not preserved: created=%v updated=%v", snap.CreatedAt, snap.UpdatedAt)
	}
	if snap.Deleted {
		t.Errorf("Deleted = true, want false")
	}
}

func issueNodeJSON(id, identifier string) string {
	return `{
		"id": "` + id + `",
		"identifier": "` + identifier + `",
		"title": "Issue ` + identifier + `",
		"description": "",
		"priority": 0,
		"state": {"id": "s1", "name": "Todo", "type": "unstarted"},
		"labels": {"nodes": []},
		"assignee": null,
		"project": null,
		"team": {"id": "team-1", "name": "Engineering", "key": "ENG"},
		"url": "https://linear.app/team/issue/` + identifier + `",
		"createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-01-02T00:00:00Z"
	}`
}

func TestSyncAdapter_ListUpdatedSince_DeltaFilterAndCursor(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		callCount++

		if reqBody.Variables["teamId"] != "ENG" {
			t.Errorf("variables[teamId] = %v, want ENG", reqBody.Variables["teamId"])
		}
		if _, ok := reqBody.Variables["since"]; !ok {
			t.Errorf("variables[since] missing, delta filter not applied")
		}

		var body string
		if callCount == 1 {
			if _, hasAfter := reqBody.Variables["after"]; hasAfter {
				t.Errorf("first page request should not set 'after'")
			}
			body = `{"issues": {"nodes": [` + issueNodeJSON("issue-1", "ENG-1") + `],
				"pageInfo": {"hasNextPage": true, "endCursor": "cursor-1"}}}`
		} else {
			if reqBody.Variables["after"] != "cursor-1" {
				t.Errorf("second page should pass through the returned cursor, got %v", reqBody.Variables["after"])
			}
			body = `{"issues": {"nodes": [` + issueNodeJSON("issue-2", "ENG-2") + `],
				"pageInfo": {"hasNextPage": false, "endCursor": ""}}}`
		}

		gqlResp := GraphQLResponse{Data: json.RawMessage(body)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gqlResp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	page1, cursor1, err := sa.ListUpdatedSince(context.Background(), "ENG", since, "")
	if err != nil {
		t.Fatalf("ListUpdatedSince page 1 failed: %v", err)
	}
	if len(page1) != 1 || page1[0].SequenceID != "ENG-1" {
		t.Fatalf("page1 = %+v, want single ENG-1 snapshot", page1)
	}
	if cursor1 != "cursor-1" {
		t.Fatalf("cursor1 = %q, want cursor-1", cursor1)
	}

	page2, cursor2, err := sa.ListUpdatedSince(context.Background(), "ENG", since, cursor1)
	if err != nil {
		t.Fatalf("ListUpdatedSince page 2 failed: %v", err)
	}
	if len(page2) != 1 || page2[0].SequenceID != "ENG-2" {
		t.Fatalf("page2 = %+v, want single ENG-2 snapshot", page2)
	}
	if cursor2 != "" {
		t.Fatalf("cursor2 = %q, want empty (exhausted)", cursor2)
	}
	if callCount != 2 {
		t.Fatalf("callCount = %d, want 2", callCount)
	}
}

func TestSyncAdapter_ListAll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if _, hasSince := reqBody.Variables["since"]; hasSince {
			t.Errorf("ListAll must not send a since filter")
		}

		body := `{"issues": {"nodes": [` + issueNodeJSON("issue-1", "ENG-1") + `],
			"pageInfo": {"hasNextPage": false, "endCursor": ""}}}`
		gqlResp := GraphQLResponse{Data: json.RawMessage(body)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gqlResp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	snaps, cursor, err := sa.ListAll(context.Background(), "ENG", "")
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snaps = %+v, want 1", snaps)
	}
	if cursor != "" {
		t.Fatalf("cursor = %q, want empty", cursor)
	}
}

func TestSyncAdapter_GetIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := `{"issue": ` + issueNodeJSON("issue-9", "ENG-9") + `}`
		gqlResp := GraphQLResponse{Data: json.RawMessage(body)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gqlResp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	snap, err := sa.GetIssue(context.Background(), "issue-9")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if snap.SequenceID != "ENG-9" {
		t.Errorf("SequenceID = %q, want ENG-9", snap.SequenceID)
	}
}

func TestSyncAdapter_UpdateFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if !strings.Contains(reqBody.Query, "issueUpdate") {
			t.Fatalf("expected issueUpdate mutation, got: %s", reqBody.Query)
		}
		if reqBody.Variables["title"] != "New title" {
			t.Errorf("variables[title] = %v, want 'New title'", reqBody.Variables["title"])
		}
		if reqBody.Variables["priority"] != float64(2) {
			t.Errorf("variables[priority] = %v, want 2 (High)", reqBody.Variables["priority"])
		}
		if _, hasLabels := reqBody.Variables["labelIds"]; hasLabels {
			t.Errorf("labelIds should not be set when labels are not in the patch")
		}

		body := `{"issueUpdate": {"success": true, "issue": ` + issueNodeJSON("issue-1", "ENG-1") + `}}`
		gqlResp := GraphQLResponse{Data: json.RawMessage(body)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gqlResp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	snap, err := sa.UpdateFields(context.Background(), "issue-1", core.FieldPatch{
		"title":    "New title",
		"priority": core.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("UpdateFields failed: %v", err)
	}
	if snap.SequenceID != "ENG-1" {
		t.Errorf("SequenceID = %q, want ENG-1", snap.SequenceID)
	}
}

func TestSyncAdapter_UpdateFields_Labels(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		queries = append(queries, reqBody.Query)

		switch {
		case strings.Contains(reqBody.Query, "issue(id:"):
			body := `{"issue": ` + issueNodeJSON("issue-1", "ENG-1") + `}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		case strings.Contains(reqBody.Query, "issueLabels"):
			body := `{"issueLabels": {"nodes": [{"id": "label-1", "name": "bug"}]}}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		case strings.Contains(reqBody.Query, "issueUpdate"):
			if reqBody.Variables["labelIds"] == nil {
				t.Errorf("labelIds should be set on the issueUpdate mutation")
			}
			body := `{"issueUpdate": {"success": true, "issue": ` + issueNodeJSON("issue-1", "ENG-1") + `}}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		default:
			t.Fatalf("unexpected query: %s", reqBody.Query)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	_, err := sa.UpdateFields(context.Background(), "issue-1", core.FieldPatch{
		"labels": []string{"bug"},
	})
	if err != nil {
		t.Fatalf("UpdateFields failed: %v", err)
	}
	if len(queries) < 3 {
		t.Fatalf("expected issue lookup + label lookup + update, got %d queries", len(queries))
	}
}

func TestSyncAdapter_TransitionState_UUID(t *testing.T) {
	const stateUUID = "12345678-90ab-cdef-1234-567890abcdef"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if strings.Contains(reqBody.Query, "workflowStates") || strings.Contains(reqBody.Query, "issue(id:") {
			t.Fatalf("state UUID input should not trigger a lookup query, got: %s", reqBody.Query)
		}
		if reqBody.Variables["stateId"] != stateUUID {
			t.Errorf("variables[stateId] = %v, want %s", reqBody.Variables["stateId"], stateUUID)
		}
		_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(`{"issueUpdate": {"success": true}}`)})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	if err := sa.TransitionState(context.Background(), "issue-1", stateUUID); err != nil {
		t.Fatalf("TransitionState failed: %v", err)
	}
}

func TestSyncAdapter_TransitionState_Name(t *testing.T) {
	const resolvedStateID = "state-uuid-1"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch {
		case strings.Contains(reqBody.Query, "issue(id:"):
			body := `{"issue": ` + issueNodeJSON("issue-1", "ENG-1") + `}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		case strings.Contains(reqBody.Query, "workflowStates"):
			if reqBody.Variables["teamKey"] != "ENG" {
				t.Errorf("variables[teamKey] = %v, want ENG", reqBody.Variables["teamKey"])
			}
			body := `{"workflowStates": {"nodes": [
				{"id": "` + resolvedStateID + `", "name": "In Progress"},
				{"id": "state-uuid-2", "name": "Done"}
			]}}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		case strings.Contains(reqBody.Query, "issueUpdate"):
			if reqBody.Variables["stateId"] != resolvedStateID {
				t.Errorf("variables[stateId] = %v, want %s", reqBody.Variables["stateId"], resolvedStateID)
			}
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(`{"issueUpdate": {"success": true}}`)})
		default:
			t.Fatalf("unexpected query: %s", reqBody.Query)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	if err := sa.TransitionState(context.Background(), "issue-1", "In Progress"); err != nil {
		t.Fatalf("TransitionState (by name) failed: %v", err)
	}
}

func TestSyncAdapter_AddComment_Idempotent(t *testing.T) {
	commentCreateCalls := 0
	existingBodies := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch {
		case strings.Contains(reqBody.Query, "comments"):
			nodes := "[]"
			if len(existingBodies) > 0 {
				b, _ := json.Marshal([]map[string]string{{"id": "c1", "body": existingBodies[0]}})
				nodes = string(b)
			}
			body := `{"issue": {"comments": {"nodes": ` + nodes + `}}}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		case strings.Contains(reqBody.Query, "commentCreate"):
			commentCreateCalls++
			postedBody, _ := reqBody.Variables["body"].(string)
			existingBodies = append(existingBodies, postedBody)
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(`{"commentCreate": {"success": true}}`)})
		default:
			t.Fatalf("unexpected query: %s", reqBody.Query)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	if err := sa.AddComment(context.Background(), "issue-1", "Synced state", "sync-key-1"); err != nil {
		t.Fatalf("AddComment (first) failed: %v", err)
	}
	if commentCreateCalls != 1 {
		t.Fatalf("commentCreateCalls = %d, want 1 after first post", commentCreateCalls)
	}

	if err := sa.AddComment(context.Background(), "issue-1", "Synced state", "sync-key-1"); err != nil {
		t.Fatalf("AddComment (retry) failed: %v", err)
	}
	if commentCreateCalls != 1 {
		t.Fatalf("commentCreateCalls = %d, want still 1 after idempotent retry", commentCreateCalls)
	}
}

func TestSyncAdapter_AddComment_IdempotentAcrossPages(t *testing.T) {
	commentCreateCalls := 0
	commentQueries := 0
	marker := syncCommentMarker("sync-key-page2")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch {
		case strings.Contains(reqBody.Query, "comments"):
			commentQueries++
			if commentQueries == 1 {
				if _, hasAfter := reqBody.Variables["after"]; hasAfter {
					t.Errorf("first comments page should not set 'after'")
				}
				body := `{"issue": {"comments": {
					"nodes": [{"id": "c1", "body": "unrelated comment"}],
					"pageInfo": {"hasNextPage": true, "endCursor": "cpage-2"}
				}}}`
				_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
				return
			}
			if reqBody.Variables["after"] != "cpage-2" {
				t.Errorf("second page should pass through the returned cursor, got %v", reqBody.Variables["after"])
			}
			body := `{"issue": {"comments": {
				"nodes": [{"id": "c2", "body": "Synced\n\n` + marker + `"}],
				"pageInfo": {"hasNextPage": false, "endCursor": ""}
			}}}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		case strings.Contains(reqBody.Query, "commentCreate"):
			commentCreateCalls++
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(`{"commentCreate": {"success": true}}`)})
		default:
			t.Fatalf("unexpected query: %s", reqBody.Query)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	if err := sa.AddComment(context.Background(), "issue-1", "Synced", "sync-key-page2"); err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}
	if commentQueries != 2 {
		t.Fatalf("commentQueries = %d, want 2 (marker found on page 2)", commentQueries)
	}
	if commentCreateCalls != 0 {
		t.Fatalf("commentCreateCalls = %d, want 0 — marker on page 2 should have been found", commentCreateCalls)
	}
}

func TestSyncAdapter_ListUpdatedSince_OnOrAfter(t *testing.T) {
	since := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if reqBody.Variables["since"] != since.Format(time.RFC3339Nano) {
			t.Errorf("variables[since] = %v, want %s (RFC3339Nano)", reqBody.Variables["since"], since.Format(time.RFC3339Nano))
		}
		if !strings.Contains(reqBody.Query, "gte") {
			t.Errorf("query must use gte (on-or-after) semantics, got: %s", reqBody.Query)
		}

		body := `{"issues": {"nodes": [` + issueNodeJSON("issue-1", "ENG-1") + `],
			"pageInfo": {"hasNextPage": false, "endCursor": ""}}}`
		_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	snaps, _, err := sa.ListUpdatedSince(context.Background(), "ENG", since, "")
	if err != nil {
		t.Fatalf("ListUpdatedSince failed: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snaps = %+v, want 1 (issue updated exactly at since should be included)", snaps)
	}
}

func TestSyncAdapter_UpdateFields_UnknownKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no request should be made when the patch has an unrecognized key")
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	_, err := sa.UpdateFields(context.Background(), "issue-1", core.FieldPatch{
		"title":      "New title",
		"assignedTo": "someone",
	})
	if err == nil {
		t.Fatal("UpdateFields with unknown key should return an error")
	}
}

func TestSyncAdapter_UpdateFields_WrongType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no request should be made when a recognized field has the wrong type")
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	_, err := sa.UpdateFields(context.Background(), "issue-1", core.FieldPatch{
		"priority": 2,
	})
	if err == nil {
		t.Fatal("UpdateFields with wrongly-typed priority should return an error")
	}
}

func TestSyncAdapter_CreateIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch {
		case strings.Contains(reqBody.Query, "teams("):
			if reqBody.Variables["key"] != "ENG" {
				t.Errorf("variables[key] = %v, want ENG", reqBody.Variables["key"])
			}
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(`{"teams": {"nodes": [{"id": "team-1"}]}}`)})
		case strings.Contains(reqBody.Query, "issueLabels"):
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(`{"issueLabels": {"nodes": []}}`)})
		case strings.Contains(reqBody.Query, "issueLabelCreate"):
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(`{"issueLabelCreate": {"success": true, "issueLabel": {"id": "label-1", "name": "bug"}}}`)})
		case strings.Contains(reqBody.Query, "issueCreate"):
			if reqBody.Variables["teamId"] != "team-1" {
				t.Errorf("variables[teamId] = %v, want team-1", reqBody.Variables["teamId"])
			}
			body := `{"issueCreate": {"success": true, "issue": ` + issueNodeJSON("issue-new", "ENG-100") + `}}`
			_ = json.NewEncoder(w).Encode(GraphQLResponse{Data: json.RawMessage(body)})
		default:
			t.Fatalf("unexpected query: %s", reqBody.Query)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	sa := NewSyncAdapter(client)

	snap, err := sa.CreateIssue(context.Background(), "ENG", core.IssueDraft{
		Title:    "New feature",
		Body:     "Description",
		Labels:   []string{"bug"},
		Priority: core.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if snap.SequenceID != "ENG-100" {
		t.Errorf("SequenceID = %q, want ENG-100", snap.SequenceID)
	}
}
