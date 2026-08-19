package jira

import (
	"encoding/json"
	"testing"
)

// TestADFText_UnmarshalJSON covers the two description shapes Jira actually
// sends: a plain string (Server/DC) and an Atlassian Document Format (ADF)
// object (Cloud's POST /rest/api/3/search/jql). Before ADFText existed,
// unmarshaling an ADF object into a plain string field rejected the entire
// API response (see GH-119 / pilot#4917).
func TestADFText_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    ADFText
		wantErr bool
	}{
		{
			name: "plain string (Server/DC)",
			json: `"Just some plain text"`,
			want: "Just some plain text",
		},
		{
			name: "empty string",
			json: `""`,
			want: "",
		},
		{
			name: "null",
			json: `null`,
			want: "",
		},
		{
			name: "ADF single paragraph",
			json: `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]}]}`,
			want: "Hello world",
		},
		{
			name: "ADF heading + paragraph + bullet list",
			json: `{
				"type": "doc",
				"version": 1,
				"content": [
					{"type": "heading", "content": [{"type": "text", "text": "Summary"}]},
					{"type": "paragraph", "content": [{"type": "text", "text": "Some details."}]},
					{"type": "bulletList", "content": [
						{"type": "listItem", "content": [
							{"type": "paragraph", "content": [{"type": "text", "text": "First item"}]}
						]},
						{"type": "listItem", "content": [
							{"type": "paragraph", "content": [{"type": "text", "text": "Second item"}]}
						]}
					]}
				]
			}`,
			want: "Summary\nSome details.\nFirst item\n\nSecond item",
		},
		{
			name: "ADF empty doc",
			json: `{"type":"doc","version":1,"content":[]}`,
			want: "",
		},
		{
			name:    "invalid JSON",
			json:    `{not valid json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ADFText
			err := json.Unmarshal([]byte(tt.json), &got)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s) expected error, got nil", tt.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s) unexpected error: %v", tt.json, err)
			}
			if got != tt.want {
				t.Errorf("UnmarshalJSON(%s) = %q, want %q", tt.json, got, tt.want)
			}
		})
	}
}

// TestADFText_UnmarshalJSON_AbsentField verifies that a struct field of type
// ADFText is left as the zero value when the JSON key is simply absent
// (rather than present-but-null).
func TestADFText_UnmarshalJSON_AbsentField(t *testing.T) {
	type wrapper struct {
		Description ADFText `json:"description"`
	}

	var w wrapper
	if err := json.Unmarshal([]byte(`{}`), &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Description != "" {
		t.Errorf("Description = %q, want empty", w.Description)
	}
}

// TestFields_UnmarshalJSON_MixedDescriptionShapes verifies that Fields
// (which embeds ADFText for Description) correctly parses both plain-string
// and ADF-object description payloads via the standard encoding/json path.
func TestFields_UnmarshalJSON_MixedDescriptionShapes(t *testing.T) {
	tests := []struct {
		name string
		json string
		want ADFText
	}{
		{
			name: "plain string description",
			json: `{"summary":"S","description":"plain text body"}`,
			want: "plain text body",
		},
		{
			name: "ADF description",
			json: `{"summary":"S","description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"rich text body"}]}]}}`,
			want: "rich text body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f Fields
			if err := json.Unmarshal([]byte(tt.json), &f); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.Description != tt.want {
				t.Errorf("Description = %q, want %q", f.Description, tt.want)
			}
		})
	}
}

// TestComment_UnmarshalJSON_MixedBodyShapes verifies that Comment (which
// embeds ADFText for Body) correctly parses both plain-string (Server/DC)
// and ADF-object (Cloud v3 AddComment/GetComments) body payloads. Before
// Comment.Body was ADFText, unmarshaling a Cloud response into a plain
// string field rejected the entire AddComment response with:
//
//	json: cannot unmarshal object into Go struct field Comment.body of type string
//
// even though the comment had already been posted successfully (see
// GH-121, same class as GH-119 / pilot#4929).
func TestComment_UnmarshalJSON_MixedBodyShapes(t *testing.T) {
	tests := []struct {
		name string
		json string
		want ADFText
	}{
		{
			name: "plain string body (Server/DC)",
			json: `{"id":"10001","body":"Test comment"}`,
			want: "Test comment",
		},
		{
			name: "ADF object body (Cloud v3)",
			json: `{
				"id": "10047",
				"body": {
					"type": "doc",
					"version": 1,
					"content": [
						{"type": "paragraph", "content": [{"type": "text", "text": "Pilot started working on this issue"}]}
					]
				}
			}`,
			want: "Pilot started working on this issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Comment
			if err := json.Unmarshal([]byte(tt.json), &c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.Body != tt.want {
				t.Errorf("Body = %q, want %q", c.Body, tt.want)
			}
		})
	}
}
