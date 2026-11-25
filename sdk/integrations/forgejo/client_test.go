package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClient_Validation(t *testing.T) {
	if _, err := NewClient("tok", "invalid", "https://fg.example.com"); err == nil {
		t.Fatal("expected error for missing owner/repo")
	}
	if _, err := NewClient("tok", "example-org/service", ""); err == nil {
		t.Fatal("expected error for empty baseURL")
	}
	c, err := NewClient("tok", "/example-org/service/", "https://fg.example.com/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.owner != "example-org" || c.repo != "service" || c.baseURL != "https://fg.example.com" {
		t.Fatalf("unexpected parsed fields: %+v", c)
	}
}

func TestClient_CreatePR_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/example-org/service/pulls" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "token test-secret-token" {
			t.Fatalf("Authorization header = %q, want %q", got, "token test-secret-token")
		}
		var req CreatePRRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Head != "pilot/PLANE-2" || req.Base != "main" || req.Title != "PLANE-2: Add README" {
			t.Fatalf("unexpected payload: %+v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(PullRequest{
			Number:  7,
			HTMLURL: "https://forgejo.example.com/example-org/service/pulls/7",
		})
	}))
	defer srv.Close()

	c, err := NewClient("test-secret-token", "example-org/service", srv.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	url, err := c.CreatePR(context.Background(), "pilot/PLANE-2", "main", "PLANE-2: Add README", "Body text")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if url != "https://forgejo.example.com/example-org/service/pulls/7" {
		t.Fatalf("got URL %q", url)
	}
}

func TestClient_CreatePR_ConflictRecoversExisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"pull request already exists"}`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/example-org/service/pulls" {
			_ = json.NewEncoder(w).Encode([]PullRequest{
				{
					Number:  5,
					HTMLURL: "https://forgejo.example.com/example-org/service/pulls/5",
					Head:    &PRBranch{Ref: "pilot/PLANE-2"},
				},
			})
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	c, err := NewClient("tok", "example-org/service", srv.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	url, err := c.CreatePR(context.Background(), "pilot/PLANE-2", "main", "Title", "Body")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if url != "https://forgejo.example.com/example-org/service/pulls/5" {
		t.Fatalf("got URL %q, want existing PR URL", url)
	}
}

func TestClient_CreatePR_ErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"token invalid"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("super-secret-xyz", "example-org/service", srv.URL)
	_, err := c.CreatePR(context.Background(), "a", "main", "t", "b")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "super-secret-xyz") {
		t.Fatalf("error leaked token: %v", err)
	}
}
