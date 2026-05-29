// Package testutil provides safe, obviously-fake credentials for use in tests.
// All constants are intentionally simple strings that cannot be mistaken for
// real secrets and will not trigger secret-scanning push-protection rules.
package testutil

const (
	// FakePlaneAPIKey is a safe test API key for Plane.so.
	FakePlaneAPIKey = "test-plane-api-key"

	// FakePlaneWebhookSecret is a safe test webhook secret for Plane.so.
	FakePlaneWebhookSecret = "test-plane-webhook-secret"
)
