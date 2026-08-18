package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestNewClient(t *testing.T) {
	client := NewClient("https://company.atlassian.net", "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.baseURL != "https://company.atlassian.net" {
		t.Errorf("client.baseURL = %s, want https://company.atlassian.net", client.baseURL)
	}
	if client.platform != PlatformCloud {
		t.Errorf("client.platform = %s, want cloud", client.platform)
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	client := NewClient("https://company.atlassian.net/", "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	if client.baseURL != "https://company.atlassian.net" {
		t.Errorf("client.baseURL = %s, want https://company.atlassian.net (no trailing slash)", client.baseURL)
	}
}

func TestAPIPath(t *testing.T) {
	tests := []struct {
		platform string
		want     string
	}{
		{PlatformCloud, "/rest/api/3"},
		{PlatformServer, "/rest/api/2"},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			client := NewClient("https://jira.example.com", "user", testutil.FakeJiraToken, tt.platform)
			got := client.apiPath()
			if got != tt.want {
				t.Errorf("apiPath() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/PROJ-42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}

		issue := Issue{
			ID:  "10001",
			Key: "PROJ-42",
			Fields: Fields{
				Summary:     "Test Issue",
				Description: "Issue description",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issue)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)

	issue, err := client.GetIssue(context.Background(), "PROJ-42")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if issue.Key != "PROJ-42" {
		t.Errorf("issue.Key = %s, want PROJ-42", issue.Key)
	}
}

func TestAddComment_Cloud(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/3/issue/PROJ-42/comment" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		bodyContent, ok := body["body"].(map[string]interface{})
		if !ok {
			t.Error("expected ADF body format for Cloud")
		}
		if bodyContent["type"] != "doc" {
			t.Errorf("expected body type 'doc', got %v", bodyContent["type"])
		}

		comment := Comment{ID: "10001", Body: "Test comment"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comment)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	_, err := client.AddComment(context.Background(), "PROJ-42", "Test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}
}

func TestAddComment_Server(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/2/issue/PROJ-42/comment" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		if body["body"] != "Test comment" {
			t.Errorf("expected plain text body for Server, got %v", body)
		}

		comment := Comment{ID: "10001", Body: "Test comment"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comment)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", testutil.FakeJiraToken, PlatformServer)
	_, err := client.AddComment(context.Background(), "PROJ-42", "Test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}
}

func TestGetTransitions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/PROJ-42/transitions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := TransitionsResponse{
			Transitions: []Transition{
				{ID: "21", Name: "Start Progress", To: Status{Name: "In Progress"}},
				{ID: "31", Name: "Done", To: Status{Name: "Done"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	transitions, err := client.GetTransitions(context.Background(), "PROJ-42")
	if err != nil {
		t.Fatalf("GetTransitions failed: %v", err)
	}
	if len(transitions) != 2 {
		t.Errorf("expected 2 transitions, got %d", len(transitions))
	}
}

func TestTransitionIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		transition := body["transition"].(map[string]interface{})
		if transition["id"] != "21" {
			t.Errorf("expected transition id '21', got %v", transition["id"])
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	err := client.TransitionIssue(context.Background(), "PROJ-42", "21")
	if err != nil {
		t.Fatalf("TransitionIssue failed: %v", err)
	}
}

func TestAddPRLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/3/issue/PROJ-42/remotelink" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body RemoteLink
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		if body.Object.URL != "https://github.com/owner/repo/pull/123" {
			t.Errorf("unexpected PR URL: %s", body.Object.URL)
		}
		if body.Object.Title != "PR #123" {
			t.Errorf("unexpected PR title: %s", body.Object.Title)
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	err := client.AddPRLink(context.Background(), "PROJ-42", "https://github.com/owner/repo/pull/123", "PR #123")
	if err != nil {
		t.Fatalf("AddPRLink failed: %v", err)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled {
		t.Errorf("default Enabled = %v, want false", cfg.Enabled)
	}
	if cfg.Platform != "cloud" {
		t.Errorf("default Platform = %s, want 'cloud'", cfg.Platform)
	}
	if cfg.TriggerLabel != "pilot" {
		t.Errorf("default TriggerLabel = %s, want 'pilot'", cfg.TriggerLabel)
	}
}

func TestPriorityFromJira(t *testing.T) {
	tests := []struct {
		name string
		want Priority
	}{
		{"Highest", PriorityHighest},
		{"Blocker", PriorityHighest},
		{"Critical", PriorityHighest},
		{"High", PriorityHigh},
		{"Major", PriorityHigh},
		{"Medium", PriorityMedium},
		{"Low", PriorityLow},
		{"Minor", PriorityLow},
		{"Lowest", PriorityLowest},
		{"Trivial", PriorityLowest},
		{"Unknown", PriorityNone},
		{"", PriorityNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PriorityFromJira(tt.name)
			if got != tt.want {
				t.Errorf("PriorityFromJira(%s) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

func TestDoRequest_ErrorHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response:   `{"id": "1", "key": "TEST-1", "self": "", "fields": {"summary": "", "description": "", "issuetype": {}, "status": {}, "labels": [], "project": {}}}`,
			wantErr:    false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   `{"errorMessages": ["Issue Does Not Exist"]}`,
			wantErr:    true,
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			response:   `{"errorMessages": ["Unauthorized"]}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClient(server.URL, "user", testutil.FakeJiraToken, PlatformCloud)
			_, err := client.GetIssue(context.Background(), "TEST-1")

			if tt.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSearchIssues_Cloud(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/3/search/jql" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		if body["jql"] != "labels = pilot" {
			t.Errorf("unexpected jql: %v", body["jql"])
		}
		if _, ok := body["fields"]; !ok {
			t.Error("expected fields in body")
		}

		resp := SearchResponse{
			Issues: []*Issue{
				{ID: "10001", Key: "PROJ-1"},
				{ID: "10002", Key: "PROJ-2"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	issues, err := client.SearchIssues(context.Background(), "labels = pilot", 50)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].Key != "PROJ-1" {
		t.Errorf("expected first issue PROJ-1, got %s", issues[0].Key)
	}
}

// TestSearchIssues_Cloud_MixedADFAndPlainDescriptions is the regression test
// for GH-119 (pilot#4917/pilot#4929): a raw v3 search/jql page mixing an ADF
// description (rich-text issue) with a plain-string description must parse
// in full, with neither issue rejecting the page nor losing its text.
func TestSearchIssues_Cloud_MixedADFAndPlainDescriptions(t *testing.T) {
	const rawPage = `{
		"issues": [
			{
				"id": "10001",
				"key": "KAN-1",
				"fields": {
					"summary": "Rich text issue",
					"description": {
						"type": "doc",
						"version": 1,
						"content": [
							{"type": "heading", "content": [{"type": "text", "text": "Overview"}]},
							{"type": "paragraph", "content": [{"type": "text", "text": "This has headings and bullets."}]},
							{"type": "bulletList", "content": [
								{"type": "listItem", "content": [
									{"type": "paragraph", "content": [{"type": "text", "text": "Point one"}]}
								]}
							]}
						]
					}
				}
			},
			{
				"id": "10002",
				"key": "KAN-2",
				"fields": {
					"summary": "Plain text issue",
					"description": "Just a plain description."
				}
			}
		]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rawPage))
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	issues, err := client.SearchIssues(context.Background(), "project = KAN", 50)
	if err != nil {
		t.Fatalf("SearchIssues failed to parse mixed ADF/plain page: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}

	wantADF := "Overview\nThis has headings and bullets.\nPoint one"
	if string(issues[0].Fields.Description) != wantADF {
		t.Errorf("issues[0].Fields.Description = %q, want %q", issues[0].Fields.Description, wantADF)
	}

	wantPlain := "Just a plain description."
	if string(issues[1].Fields.Description) != wantPlain {
		t.Errorf("issues[1].Fields.Description = %q, want %q", issues[1].Fields.Description, wantPlain)
	}
}

// TestSearchIssues_LegacySearchGone410_HintsCloudPlatform verifies that a 410
// Gone from the legacy GET /rest/api/2/search endpoint (which Jira Cloud
// returns since removing it in May 2025) is wrapped with an actionable hint
// telling the operator to set platform: cloud.
func TestSearchIssues_LegacySearchGone410_HintsCloudPlatform(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"errorMessages":["This endpoint is no longer available"]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", testutil.FakeJiraToken, PlatformServer)
	_, err := client.SearchIssues(context.Background(), "labels = pilot", 50)
	if err == nil {
		t.Fatal("expected error for 410 response")
	}
	if !strings.Contains(err.Error(), "platform: cloud") {
		t.Errorf("error = %q, want it to contain hint %q", err.Error(), "platform: cloud")
	}
}

func TestSearchIssues_Server(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/2/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.RawQuery == "" {
			t.Error("expected jql query param")
		}

		resp := SearchResponse{
			Issues: []*Issue{{ID: "10001", Key: "PROJ-1"}},
			Total:  1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", testutil.FakeJiraToken, PlatformServer)
	issues, err := client.SearchIssues(context.Background(), "labels = pilot", 50)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
}

func TestCreateIssue_Cloud(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/3/issue" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		fields, ok := body["fields"].(map[string]interface{})
		if !ok {
			t.Fatal("expected fields object")
		}
		if fields["summary"] != "New issue" {
			t.Errorf("summary = %v, want %q", fields["summary"], "New issue")
		}
		project, ok := fields["project"].(map[string]interface{})
		if !ok || project["key"] != "PROJ" {
			t.Errorf("project.key = %v, want PROJ", fields["project"])
		}
		issuetype, ok := fields["issuetype"].(map[string]interface{})
		if !ok || issuetype["name"] != "Task" {
			t.Errorf("issuetype.name = %v, want Task", fields["issuetype"])
		}
		desc, ok := fields["description"].(map[string]interface{})
		if !ok || desc["type"] != "doc" {
			t.Errorf("expected ADF description for Cloud, got %v", fields["description"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(&IssueCreateResponse{ID: "10050", Key: "PROJ-50"})
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	created, err := client.CreateIssue(context.Background(), "PROJ", "Task", "New issue", "Description", []string{"bug"})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if created.Key != "PROJ-50" {
		t.Errorf("Key = %q, want PROJ-50", created.Key)
	}
}

func TestCreateIssue_Server_PlainDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		fields := body["fields"].(map[string]interface{})
		if fields["description"] != "Description" {
			t.Errorf("expected plain-text description for Server, got %v", fields["description"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(&IssueCreateResponse{ID: "10050", Key: "PROJ-50"})
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", testutil.FakeJiraToken, PlatformServer)
	if _, err := client.CreateIssue(context.Background(), "PROJ", "Task", "New issue", "Description", nil); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
}

func TestUpdateFields_PartialPatch(t *testing.T) {
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/3/issue/PROJ-42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	err := client.UpdateFields(context.Background(), "PROJ-42", map[string]interface{}{
		"summary": "Updated title",
	})
	if err != nil {
		t.Fatalf("UpdateFields failed: %v", err)
	}

	fields, ok := gotBody["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fields object in request")
	}
	if fields["summary"] != "Updated title" {
		t.Errorf("summary = %v, want %q", fields["summary"], "Updated title")
	}
	if _, hasDescription := fields["description"]; hasDescription {
		t.Errorf("partial patch should not send unset field %q", "description")
	}
}

func TestGetComments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/PROJ-42/comment" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comments":[{"id":"1","body":"unrelated"},{"id":"2","body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"synced\n\n<!-- pilot-op:key-123 -->"}]}]}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	comments, err := client.GetComments(context.Background(), "PROJ-42")
	if err != nil {
		t.Fatalf("GetComments failed: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	found := false
	for _, c := range comments {
		if strings.Contains(string(c.Body), "<!-- pilot-op:key-123 -->") {
			found = true
		}
	}
	if !found {
		t.Error("expected to find idempotency marker in an ADF comment body")
	}
}

// TestDoRequest_RetriesRateLimit_HonorsRetryAfter verifies that a 429 with a
// Retry-After header is retried and the header value is honored rather than
// falling back to exponential backoff.
func TestDoRequest_RetriesRateLimit_HonorsRetryAfter(t *testing.T) {
	calls := 0
	var firstCallTime, secondCallTime time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			firstCallTime = time.Now()
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errorMessages":["rate limited"]}`))
			return
		}
		secondCallTime = time.Now()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&Issue{ID: "1", Key: "PROJ-1"})
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	issue, err := client.GetIssue(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if issue.Key != "PROJ-1" {
		t.Errorf("Key = %q, want PROJ-1", issue.Key)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls (1 retry), got %d", calls)
	}
	if secondCallTime.Before(firstCallTime) {
		t.Error("second call should happen after first")
	}
}

// TestDoRequest_AuthErrorShortCircuits verifies that a 401/403 response does
// not get retried — retrying with a dead token cannot succeed.
func TestDoRequest_AuthErrorShortCircuits(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorMessages":["Unauthorized"]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", testutil.FakeJiraToken, PlatformCloud)
	client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	_, err := client.GetIssue(context.Background(), "PROJ-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on auth error), got %d", calls)
	}
}
