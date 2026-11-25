package forgejo

// Config holds Forgejo/Gitea adapter configuration.
type Config struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
}

// DefaultConfig returns default Forgejo configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled: false,
	}
}

// PullRequest represents a Forgejo/Gitea pull request response.
type PullRequest struct {
	ID      int64   `json:"id"`
	Number  int     `json:"number"`
	HTMLURL string  `json:"html_url"`
	Title   string  `json:"title"`
	State   string  `json:"state"`
	Head    *PRBranch `json:"head,omitempty"`
	Base    *PRBranch `json:"base,omitempty"`
}

// PRBranch represents a branch reference on a Forgejo pull request.
type PRBranch struct {
	Label string `json:"label"`
	Ref   string `json:"ref"`
	SHA   string `json:"sha"`
}

// CreatePRRequest is the payload for POST /api/v1/repos/{owner}/{repo}/pulls.
type CreatePRRequest struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Head  string `json:"head"`
	Base  string `json:"base"`
}

// Repository represents basic repository metadata from Forgejo.
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
}
