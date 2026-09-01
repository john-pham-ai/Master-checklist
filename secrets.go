package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// appName must match the `name` field in project.toml; apps-platform prefixes
// secret names with it for isolation (see README "Secrets" section).
const appName = "master-checklist"

// tokenSource lazily fetches and caches a named Secret Manager secret.
// Fetching is deferred until a request actually needs it, so the server can
// start (and pass Cloud Run's health check) even before the secret has been
// set.
type tokenSource struct {
	secretName string // e.g. "confluence-token"; apps-platform prefixes it with appName
	dryRun     bool
	dryRunVal  string

	mu    sync.Mutex
	cache string
}

func newTokenSource(secretName string, dryRun bool) *tokenSource {
	return &tokenSource{secretName: secretName, dryRun: dryRun, dryRunVal: "dry-run-" + secretName}
}

func (t *tokenSource) Get(ctx context.Context) (string, error) {
	if t.dryRun {
		return t.dryRunVal, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache != "" {
		return t.cache, nil
	}

	projectID := os.Getenv("PROJECT_ID")
	if projectID == "" {
		return "", fmt.Errorf("PROJECT_ID is not set")
	}

	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("secretmanager.NewClient: %w", err)
	}
	defer client.Close()

	secretPath := fmt.Sprintf("projects/%s/secrets/%s-%s/versions/latest", projectID, appName, t.secretName)
	req := &secretmanagerpb.AccessSecretVersionRequest{Name: secretPath}
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AccessSecretVersion: %w", err)
	}

	t.cache = string(result.Payload.Data)
	return t.cache, nil
}

type config struct {
	BaseURL               string
	SpaceKey              string
	ParentPageID          string
	CandidateParentPageID string
	BotEmail              string
	Addr                  string
	DryRun                bool
	Token                 *tokenSource

	GithubOwner string
	GithubRepo  string
	GithubToken *tokenSource
}

func loadConfig() config {
	dryRun := os.Getenv("CONFLUENCE_DRY_RUN") == "true"
	return config{
		BaseURL:               envOrDefault("CONFLUENCE_BASE_URL", "https://appliedintuition.atlassian.net/wiki"),
		SpaceKey:              envOrDefault("CONFLUENCE_SPACE_KEY", "NEURON"),
		ParentPageID:          envOrDefault("CONFLUENCE_PARENT_PAGE_ID", "2693234852"),
		CandidateParentPageID: envOrDefault("CONFLUENCE_CANDIDATE_PARENT_PAGE_ID", "2909896800"),
		BotEmail:              os.Getenv("CONFLUENCE_BOT_EMAIL"),
		Addr:                  listenAddr(),
		DryRun:                dryRun,
		Token:                 newTokenSource("confluence-token", dryRun),

		GithubOwner: envOrDefault("GITHUB_TAG_REPO_OWNER", "Ext-Applied-Frontier"),
		GithubRepo:  envOrDefault("GITHUB_TAG_REPO_NAME", "brain2"),
		GithubToken: newTokenSource("github-token", dryRun),
	}
}

// listenAddr honors Cloud Run's PORT env var, falling back to ADDR/8080 for
// local development.
func listenAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return envOrDefault("ADDR", ":8080")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
