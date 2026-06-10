# Polling Example

Poll open issues from a GitHub repository by label and print each result.

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	client := github.NewClient(github.Config{
		Token:  os.Getenv("GITHUB_TOKEN"),
		Owner:  "my-org",
		Repo:   "my-repo",
		Logger: logger,
	})

	ctx := context.Background()
	issues, err := client.FetchIssues(ctx, github.FetchOptions{
		State:  "open",
		Labels: []string{"bug"},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch error: %v\n", err)
		os.Exit(1)
	}

	for _, issue := range issues {
		fmt.Printf("#%d  %s\n", issue.Number, issue.Title)
	}
}
```

**What this does:**

1. Builds a GitHub client from a personal access token and repo coordinates.
2. Calls `FetchIssues` with `state=open` and a label filter.
3. Prints the issue number and title for every result.

Set `GITHUB_TOKEN` to a token with `repo` (or `public_repo`) scope before running.
