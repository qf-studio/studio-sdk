// Package testutil provides shared test helpers for sdk adapter implementations.
// All constants here are obviously fake and must never resemble real credentials.
package testutil

const (
	FakePlaneAPIKey = "test-plane-api-key"

	FakeGitLabToken         = "test-gitlab-token"
	FakeGitLabWebhookSecret = "test-gitlab-webhook-secret"

	FakeAzureDevOpsPAT           = "test-azure-devops-pat"
	FakeAzureDevOpsWebhookSecret = "test-azure-devops-webhook-secret"

	FakeGitHubToken         = "test-github-token"
	FakeGitHubWebhookSecret = "test-github-webhook-secret"
)
