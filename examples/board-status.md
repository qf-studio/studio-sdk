# Board-Status Example

Update a GitHub Project (v2) board's Status field for a specific issue.

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func main() {
	client := github.NewClient(github.Config{
		Token: os.Getenv("GITHUB_TOKEN"),
		Owner: "my-org",
		Repo:  "my-repo",
	})

	ctx := context.Background()

	// Resolve the project item for the given issue URL.
	itemID, err := client.ResolveProjectItem(ctx, github.ProjectItemQuery{
		ProjectNumber: 1,
		IssueURL:      "https://github.com/my-org/my-repo/issues/42",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve error: %v\n", err)
		os.Exit(1)
	}

	// Set the single-select Status field to "In Progress".
	err = client.SetProjectField(ctx, github.SetFieldInput{
		ProjectNumber: 1,
		ItemID:        itemID,
		FieldName:     "Status",
		Value:         "In Progress",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "set field error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Status updated.")
}
```

**What this does:**

1. Calls `ResolveProjectItem` to find the board card linked to an issue URL.
2. Calls `SetProjectField` to write a new value to the `Status` single-select field.

The token needs `project` scope in addition to `repo` for project mutations.
