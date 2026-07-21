package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// TestCheckUnsourcedLabeledIssues covers the three states an open, dispatch-labeled
// issue can be in relative to a board source: correctly sourced (no warning), absent
// from the board entirely, and present on the board but in the wrong status column.
// GH-4488: the latter two were previously silent — no log line distinguished a
// healthy-but-empty poller from one that had stopped seeing real work.
func TestCheckUnsourcedLabeledIssues(t *testing.T) {
	const pilotLabel = "pilot"

	tests := []struct {
		name          string
		boardNodes    []map[string]interface{}
		wantUnsourced int
		wantWarn      bool
	}{
		{
			name:          "labeled issue absent from board warns and sets gauge",
			boardNodes:    nil,
			wantUnsourced: 1,
			wantWarn:      true,
		},
		{
			name: "labeled issue on board but wrong status warns and sets gauge",
			boardNodes: []map[string]interface{}{
				issueNode(42, "I_42", "t", "", "OPEN", "org/repo", "In Progress", pilotLabel),
			},
			wantUnsourced: 1,
			wantWarn:      true,
		},
		{
			name: "labeled issue correctly sourced does not warn",
			boardNodes: []map[string]interface{}{
				issueNode(42, "I_42", "t", "", "OPEN", "org/repo", "Todo", pilotLabel),
			},
			wantUnsourced: 0,
			wantWarn:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labeledIssue := &Issue{Number: 42, Title: "t", Labels: []Label{{Name: pilotLabel}}}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasPrefix(r.URL.Path, "/repos/") {
					_, _ = w.Write(mustJSON([]*Issue{labeledIssue}))
					return
				}
				var req GraphQLRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode graphql request: %v", err)
				}
				switch {
				case strings.Contains(req.Query, "organization"):
					_, _ = w.Write([]byte(orgProjectResp("PVT_1")))
				case strings.Contains(req.Query, "items(first: 100"):
					_, _ = w.Write([]byte(itemsResp(tt.boardNodes, false, "")))
				default:
					t.Fatalf("unexpected graphql query: %s", req.Query)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			src := NewProjectBoardSource(client, &ProjectBoardConfig{
				ProjectNumber: 1,
				StatusField:   "Status",
				SourceEnabled: true,
				SourceStatus:  "Todo",
			}, "org", "repo")

			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			m := newFakePollerMetrics()

			poller, err := NewPoller(client, "org/repo", pilotLabel, 0,
				WithProjectBoardSource(src),
				WithPollerLogger(logger),
				WithPollerMetrics(m),
			)
			if err != nil {
				t.Fatalf("NewPoller: %v", err)
			}

			poller.checkUnsourcedLabeledIssues(context.Background())

			m.mu.Lock()
			gotUnsourced := m.unsourcedLabeled["org/repo"]
			m.mu.Unlock()
			if gotUnsourced != tt.wantUnsourced {
				t.Errorf("gauge = %d, want %d", gotUnsourced, tt.wantUnsourced)
			}

			gotWarn := strings.Contains(buf.String(), "not board-sourced")
			if gotWarn != tt.wantWarn {
				t.Errorf("warn logged = %v, want %v (log: %s)", gotWarn, tt.wantWarn, buf.String())
			}
		})
	}
}

// TestCheckUnsourcedLabeledIssues_WarnsOnceThenClears drives the exact acceptance
// scenario from GH-4488: a labeled issue absent from the board warns on the first
// poll-session, does NOT re-warn on a subsequent session while still unresolved
// (once per session, not once per tick), and clears both the WARN and the gauge
// once the card is added to the source-status column.
func TestCheckUnsourcedLabeledIssues_WarnsOnceThenClears(t *testing.T) {
	const pilotLabel = "pilot"
	labeledIssue := &Issue{Number: 55, Title: "t", Labels: []Label{{Name: pilotLabel}}}

	var boardStatus string // "" = absent from the board entirely
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			_, _ = w.Write(mustJSON([]*Issue{labeledIssue}))
			return
		}
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode graphql request: %v", err)
		}
		switch {
		case strings.Contains(req.Query, "organization"):
			_, _ = w.Write([]byte(orgProjectResp("PVT_1")))
		case strings.Contains(req.Query, "items(first: 100"):
			var nodes []map[string]interface{}
			if boardStatus != "" {
				nodes = []map[string]interface{}{
					issueNode(labeledIssue.Number, "I_55", "t", "", "OPEN", "org/repo", boardStatus, pilotLabel),
				}
			}
			_, _ = w.Write([]byte(itemsResp(nodes, false, "")))
		default:
			t.Fatalf("unexpected graphql query: %s", req.Query)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 1,
		StatusField:   "Status",
		SourceEnabled: true,
		SourceStatus:  "Todo",
	}, "org", "repo")

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	m := newFakePollerMetrics()

	poller, err := NewPoller(client, "org/repo", pilotLabel, 0,
		WithProjectBoardSource(src),
		WithPollerLogger(logger),
		WithPollerMetrics(m),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	// Session 1: absent from board — warns, gauge=1.
	poller.checkUnsourcedLabeledIssues(context.Background())
	if got := strings.Count(buf.String(), "not board-sourced"); got != 1 {
		t.Fatalf("session1: warn count = %d, want 1 (log: %s)", got, buf.String())
	}
	if got := m.unsourcedLabeled["org/repo"]; got != 1 {
		t.Fatalf("session1: gauge = %d, want 1", got)
	}

	// Session 2: still absent — must NOT re-warn (once per session while unresolved).
	buf.Reset()
	poller.checkUnsourcedLabeledIssues(context.Background())
	if got := strings.Count(buf.String(), "not board-sourced"); got != 0 {
		t.Fatalf("session2: warn count = %d, want 0 (log: %s)", got, buf.String())
	}
	if got := m.unsourcedLabeled["org/repo"]; got != 1 {
		t.Fatalf("session2: gauge = %d, want 1 (still unsourced)", got)
	}

	// Session 3: card added to Todo — dispatch can now proceed, WARN clears.
	boardStatus = "Todo"
	buf.Reset()
	poller.checkUnsourcedLabeledIssues(context.Background())
	if got := strings.Count(buf.String(), "not board-sourced"); got != 0 {
		t.Fatalf("session3: warn count = %d, want 0 (log: %s)", got, buf.String())
	}
	if got := m.unsourcedLabeled["org/repo"]; got != 0 {
		t.Fatalf("session3: gauge = %d, want 0 (resolved)", got)
	}
}

// TestCheckUnsourcedLabeledIssues_NoBoardSource verifies the check is a pure no-op
// (no network calls) when board sourcing isn't configured — label-mode dispatch has
// nothing to cross-check.
func TestCheckUnsourcedLabeledIssues_NoBoardSource(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, err := NewPoller(client, "org/repo", "pilot", 0)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.checkUnsourcedLabeledIssues(context.Background())

	if calls != 0 {
		t.Errorf("expected no network calls when projectBoardSource is nil, got %d", calls)
	}
}

// TestClassifyBoardSyncAuthError is a table-driven test for the pure error
// classifier that decides whether a board-sync failure is auth/scope-class
// (worth alerting on) versus a benign/unrelated error (stays WARN-only).
func TestClassifyBoardSyncAuthError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantOK     bool
		wantDetail string
	}{
		{
			name:   "nil error",
			err:    nil,
			wantOK: false,
		},
		{
			name:       "AuthError is auth-class",
			err:        &AuthError{Message: "Bad credentials"},
			wantOK:     true,
			wantDetail: "Bad credentials",
		},
		{
			name:   "AuthError flattened to a plain string is no longer classifiable",
			err:    errors.New("get issue node id: " + (&AuthError{Message: "Bad credentials"}).Error()),
			wantOK: false, // errors.New loses the *AuthError type; errors.As can't recover it, and the message has no INSUFFICIENT_SCOPES substring
		},
		{
			name:       "INSUFFICIENT_SCOPES graphql error is auth-class",
			err:        errors.New("set project item field value: graphql error: INSUFFICIENT_SCOPES: 'projectV2' requires read:project, token has [gist, read:org, repo, workflow]"),
			wantOK:     true,
			wantDetail: "set project item field value: graphql error: INSUFFICIENT_SCOPES: 'projectV2' requires read:project, token has [gist, read:org, repo, workflow]",
		},
		{
			name:   "unrelated error stays WARN-only",
			err:    errors.New("get issue project item: graphql error: NOT_FOUND: Could not resolve to an Issue"),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail, ok := classifyBoardSyncAuthError(tt.err)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tt.wantDetail)
			}
		})
	}
}

// TestAlertBoardSyncAuthError_FiresOncePerBoot drives syncBoardStatus through
// repeated INSUFFICIENT_SCOPES failures (as a persistently under-scoped token
// would produce on every card update) and asserts the alert hook fires exactly
// once, not once per item/update, per GH-4488.
func TestAlertBoardSyncAuthError_FiresOncePerBoot(t *testing.T) {
	const projectID = "PVT_1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode graphql request: %v", err)
		}
		switch {
		case strings.Contains(req.Query, "organization"):
			_, _ = w.Write([]byte(orgProjectResp(projectID)))
		case strings.Contains(req.Query, "field(name:"):
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"field": map[string]interface{}{
							"id": "FIELD_status",
							"options": []map[string]interface{}{
								{"id": "OPT_progress", "name": "In Progress"},
							},
						},
					},
				},
			}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)
		case strings.Contains(req.Query, "projectItems(first:"):
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"projectItems": map[string]interface{}{
							"nodes": []map[string]interface{}{
								{
									"id":               "PVTI_1",
									"project":          map[string]interface{}{"id": projectID},
									"fieldValueByName": map[string]interface{}{"optionId": "OPT_other"},
								},
							},
						},
					},
				},
			}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)
		case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
			_, _ = w.Write([]byte(`{"errors":[{"message":"'projectV2' requires read:project, token has [gist, read:org, repo, workflow]","type":"INSUFFICIENT_SCOPES"}]}`))
		default:
			t.Fatalf("unexpected graphql query: %s", req.Query)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	bsync := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "org")

	var alertCount int
	var lastMsg string
	poller, err := NewPoller(client, "org/repo", "pilot", 0,
		WithBoardSync(bsync, "In Progress"),
		WithBoardSyncAuthAlert(func(err error) {
			alertCount++
			lastMsg = err.Error()
		}),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	issue := &Issue{Number: 1, NodeID: "ISSUE_1", Title: "t"}
	poller.syncBoardStatusInProgress(context.Background(), issue)
	poller.syncBoardStatusInProgress(context.Background(), issue)
	poller.syncBoardStatusInProgress(context.Background(), issue)

	if alertCount != 1 {
		t.Fatalf("alertCount = %d, want 1 (once per boot, not once per update)", alertCount)
	}
	if !strings.Contains(lastMsg, "read:project") {
		t.Errorf("alert message missing missing-scope detail, got: %s", lastMsg)
	}
}
