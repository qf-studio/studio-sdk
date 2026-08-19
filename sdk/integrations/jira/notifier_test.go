package jira

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

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func newTestNotifierServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	return client, server
}

func TestNotifyTaskStarted_WithTransitionID(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1", Body: "ok"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "21", "31")
	err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123")
	if err != nil {
		t.Fatalf("NotifyTaskStarted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 API calls, got %d: %v", len(calls), calls)
	}
	if calls[0] != "POST /rest/api/3/issue/PROJ-42/transitions" {
		t.Errorf("first call should be transition, got %s", calls[0])
	}
	if calls[1] != "POST /rest/api/3/issue/PROJ-42/comment" {
		t.Errorf("second call should be comment, got %s", calls[1])
	}
}

// TestNotifyTaskStarted_CloudADFCommentResponse verifies NotifyTaskStarted
// succeeds when the Cloud API echoes the posted comment back as an ADF
// object (the real-world response shape). Before Comment.Body was ADFText,
// this response failed to unmarshal and NotifyTaskStarted always returned
// an error despite the comment having been posted successfully (see
// GH-121, live-verified on the KAN-6 e2e card).
func TestNotifyTaskStarted_CloudADFCommentResponse(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "10047",
				"body": {
					"type": "doc",
					"version": 1,
					"content": [
						{"type": "paragraph", "content": [{"type": "text", "text": "started"}]}
					]
				}
			}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "21", "31")
	if err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123"); err != nil {
		t.Fatalf("NotifyTaskStarted failed on Cloud ADF comment response: %v", err)
	}
}

func TestNotifyTaskStarted_WithoutTransitionID(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodGet:
			resp := TransitionsResponse{
				Transitions: []Transition{
					{ID: "21", Name: "Start Progress", To: Status{Name: "In Progress", StatusCategory: StatusCategory{Key: "indeterminate"}}},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1", Body: "ok"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123")
	if err != nil {
		t.Fatalf("NotifyTaskStarted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("expected 3 API calls, got %d: %v", len(calls), calls)
	}
}

func TestNotifyTaskCompleted_Success(t *testing.T) {
	var mu sync.Mutex
	var commentBody string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Extract text from ADF
			if doc, ok := body["body"].(map[string]interface{}); ok {
				if content, ok := doc["content"].([]interface{}); ok && len(content) > 0 {
					if para, ok := content[0].(map[string]interface{}); ok {
						if inner, ok := para["content"].([]interface{}); ok && len(inner) > 0 {
							if text, ok := inner[0].(map[string]interface{}); ok {
								mu.Lock()
								commentBody, _ = text["text"].(string)
								mu.Unlock()
							}
						}
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "31")
	err := notifier.NotifyTaskCompleted(context.Background(), "PROJ-42", "https://github.com/org/repo/pull/99", "Added feature X")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(commentBody, "pull/99") {
		t.Errorf("comment should contain PR URL, got: %s", commentBody)
	}
	if !strings.Contains(commentBody, "Added feature X") {
		t.Errorf("comment should contain summary, got: %s", commentBody)
	}
}

func TestNotifyTaskCompleted_NoPRURL(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/comment"):
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			doc := body["body"].(map[string]interface{})
			content := doc["content"].([]interface{})
			para := content[0].(map[string]interface{})
			inner := para["content"].([]interface{})
			text := inner[0].(map[string]interface{})["text"].(string)
			if strings.Contains(text, "Pull Request") {
				t.Errorf("comment should not contain PR section when no URL, got: %s", text)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(TransitionsResponse{
					Transitions: []Transition{{ID: "31", Name: "Done", To: Status{Name: "Done", StatusCategory: StatusCategory{Key: "done"}}}},
				})
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskCompleted(context.Background(), "PROJ-42", "", "")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}
}

func TestNotifyTaskFailed(t *testing.T) {
	var commentPosted bool

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost {
			commentPosted = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskFailed(context.Background(), "PROJ-42", "build failed")
	if err != nil {
		t.Fatalf("NotifyTaskFailed failed: %v", err)
	}
	if !commentPosted {
		t.Error("expected comment to be posted")
	}
}

func TestNotifyTaskFailed_APIError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errorMessages":["Internal Server Error"]}`))
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskFailed(context.Background(), "PROJ-42", "build failed")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "failed to add failure comment") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNotifyProgress(t *testing.T) {
	tests := []struct {
		phase string
	}{
		{"exploring"},
		{"implementing"},
		{"testing"},
		{"committing"},
		{"unknown-phase"},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			})
			defer server.Close()

			notifier := NewNotifier(client, "", "")
			err := notifier.NotifyProgress(context.Background(), "PROJ-42", tt.phase, "details here")
			if err != nil {
				t.Fatalf("NotifyProgress(%s) failed: %v", tt.phase, err)
			}
		})
	}
}

func TestNotifyProgress_APIError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errorMessages":["error"]}`))
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyProgress(context.Background(), "PROJ-42", "impl", "details")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

func TestLinkPR(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/remotelink" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.LinkPR(context.Background(), "PROJ-42", 99, "https://github.com/org/repo/pull/99")
	if err != nil {
		t.Fatalf("LinkPR failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (remotelink + comment), got %d: %v", len(calls), calls)
	}
}

func TestLinkPR_RemoteLinkError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errorMessages":["error"]}`))
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.LinkPR(context.Background(), "PROJ-42", 99, "https://github.com/org/repo/pull/99")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to add PR link") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNotifyTaskStarted_LocalizedWorkflow verifies the in-progress transition
// resolves by status category ("indeterminate"), not by the English status
// name "In Progress", when no transition ID is configured. Before this fix,
// TransitionIssueTo("In Progress") was used, which is guaranteed to never
// match on a localized workflow like the live Russian-locale validation
// site (statuses such as "В работе" / "К выполнению") — see KAN-6.
func TestNotifyTaskStarted_LocalizedWorkflow(t *testing.T) {
	var mu sync.Mutex
	var transitionedID string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TransitionsResponse{
				Transitions: []Transition{
					{ID: "1", Name: "К выполнению", To: Status{Name: "К выполнению", StatusCategory: StatusCategory{Key: "new"}}},
					{ID: "11", Name: "Взять в работу", To: Status{Name: "В работе", StatusCategory: StatusCategory{Key: "indeterminate"}}},
					{ID: "21", Name: "Готово", To: Status{Name: "Готово", StatusCategory: StatusCategory{Key: "done"}}},
				},
			})
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			transitionedID = body["transition"].(map[string]interface{})["id"].(string)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	if err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123"); err != nil {
		t.Fatalf("NotifyTaskStarted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if transitionedID != "11" {
		t.Errorf("expected transition to indeterminate-category transition ID 11, got %q", transitionedID)
	}
}

// TestNotifyTaskCompleted_LocalizedWorkflow mirrors the above for the Done
// leg.
func TestNotifyTaskCompleted_LocalizedWorkflow(t *testing.T) {
	var mu sync.Mutex
	var transitionedID string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TransitionsResponse{
				Transitions: []Transition{
					{ID: "11", Name: "Взять в работу", To: Status{Name: "В работе", StatusCategory: StatusCategory{Key: "indeterminate"}}},
					{ID: "21", Name: "Завершить", To: Status{Name: "Готово", StatusCategory: StatusCategory{Key: "done"}}},
				},
			})
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			transitionedID = body["transition"].(map[string]interface{})["id"].(string)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	if err := notifier.NotifyTaskCompleted(context.Background(), "PROJ-42", "", "done"); err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if transitionedID != "21" {
		t.Errorf("expected transition to done-category transition ID 21, got %q", transitionedID)
	}
}

// TestNotifyTaskCompleted_CommentFailureStillTransitions verifies that a
// comment failure (e.g. the pre-#122 ADF parse bug, or any other transient
// error) no longer aborts the Done transition. Before this fix,
// NotifyTaskCompleted returned early on the comment error and the
// transition was never attempted.
func TestNotifyTaskCompleted_CommentFailureStillTransitions(t *testing.T) {
	var mu sync.Mutex
	var transitionPosted bool

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errorMessages":["error"]}`))
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			mu.Lock()
			transitionPosted = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "31")
	err := notifier.NotifyTaskCompleted(context.Background(), "PROJ-42", "", "summary")
	if err == nil {
		t.Fatal("expected error surfaced from comment failure")
	}
	if !strings.Contains(err.Error(), "failed to add completion comment") {
		t.Errorf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !transitionPosted {
		t.Error("expected Done transition to still be attempted despite comment failure")
	}
}

// TestTransitionToCategory covers the category-matching resolution logic
// directly: a single match, an ambiguous match (multiple transitions target
// the same category — first one wins, logged), and no match at all (WARN
// only, the caller does not abort).
func TestTransitionToCategory(t *testing.T) {
	tests := []struct {
		name        string
		transitions []Transition
		category    string
		wantID      string
		wantErr     bool
	}{
		{
			name: "category match",
			transitions: []Transition{
				{ID: "11", Name: "Start", To: Status{Name: "In Progress", StatusCategory: StatusCategory{Key: "indeterminate"}}},
				{ID: "21", Name: "Finish", To: Status{Name: "Done", StatusCategory: StatusCategory{Key: "done"}}},
			},
			category: "done",
			wantID:   "21",
		},
		{
			name: "ambiguous match picks first",
			transitions: []Transition{
				{ID: "31", Name: "Close as fixed", To: Status{Name: "Done", StatusCategory: StatusCategory{Key: "done"}}},
				{ID: "32", Name: "Close as won't fix", To: Status{Name: "Won't Do", StatusCategory: StatusCategory{Key: "done"}}},
			},
			category: "done",
			wantID:   "31",
		},
		{
			name: "no candidate",
			transitions: []Transition{
				{ID: "1", Name: "To Do", To: Status{Name: "To Do", StatusCategory: StatusCategory{Key: "new"}}},
			},
			category: "done",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var transitionedID string

			client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodGet:
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(TransitionsResponse{Transitions: tt.transitions})
				case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
					var body map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					transitionedID = body["transition"].(map[string]interface{})["id"].(string)
					mu.Unlock()
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			defer server.Close()

			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))
			notifier := NewNotifier(client, "", "", WithLogger(logger))

			err := notifier.transitionToCategory(context.Background(), "PROJ-42", tt.category)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error for no-candidate case")
				}
				mu.Lock()
				posted := transitionedID != ""
				mu.Unlock()
				if posted {
					t.Error("expected no transition POST when no candidate matches")
				}
				return
			}

			if err != nil {
				t.Fatalf("transitionToCategory failed: %v", err)
			}

			mu.Lock()
			got := transitionedID
			mu.Unlock()
			if got != tt.wantID {
				t.Errorf("expected transition ID %q, got %q", tt.wantID, got)
			}
		})
	}
}

// TestNotifyTaskStarted_NoCandidateTransition_Warns verifies that when no
// transition targets the in-progress category, NotifyTaskStarted logs a WARN
// but does not fail the call (mirroring the existing behavior for the
// configured-ID path, and consistent with every other transition failure in
// this notifier being non-fatal).
func TestNotifyTaskStarted_NoCandidateTransition_Warns(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TransitionsResponse{
				Transitions: []Transition{
					{ID: "1", Name: "To Do", To: Status{Name: "To Do", StatusCategory: StatusCategory{Key: "new"}}},
				},
			})
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	notifier := NewNotifier(client, "", "", WithLogger(logger))

	if err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123"); err != nil {
		t.Fatalf("NotifyTaskStarted should not fail when no transition candidate is found, got: %v", err)
	}

	if !strings.Contains(logBuf.String(), "failed to transition issue to In Progress") {
		t.Errorf("expected WARN log about missing transition, got: %s", logBuf.String())
	}
}

func TestNotifyTaskStarted_CommentError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/comment"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errorMessages":["error"]}`))
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "21", "")
	err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123")
	if err == nil {
		t.Fatal("expected error when comment fails")
	}
	if !strings.Contains(err.Error(), "failed to add start comment") {
		t.Errorf("unexpected error: %v", err)
	}
}
