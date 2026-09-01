package main

import (
	"context"
	"fmt"
	"os"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// appName must match the `name` field in project.toml; apps-platform prefixes
// secret names with it for isolation (see README "Secrets" section).
const appName = "master-checklist"

// loadConfluenceToken fetches the confluence-token secret from Secret Manager.
// When CONFLUENCE_DRY_RUN=true it is skipped entirely (see README "Local dev").
func loadConfluenceToken(ctx context.Context) (string, error) {
	if os.Getenv("CONFLUENCE_DRY_RUN") == "true" {
		return "dry-run-token", nil
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

	secretPath := fmt.Sprintf("projects/%s/secrets/%s-confluence-token/versions/latest", projectID, appName)
	req := &secretmanagerpb.AccessSecretVersionRequest{Name: secretPath}
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AccessSecretVersion: %w", err)
	}
	return string(result.Payload.Data), nil
}

type config struct {
	BaseURL      string
	SpaceKey     string
	ParentPageID string
	BotEmail     string
	APIToken     string
	Addr         string
	DryRun       bool
}

func loadConfig(ctx context.Context) (config, error) {
	cfg := config{
		BaseURL:      envOrDefault("CONFLUENCE_BASE_URL", "https://appliedintuition.atlassian.net/wiki"),
		SpaceKey:     envOrDefault("CONFLUENCE_SPACE_KEY", "NEURON"),
		ParentPageID: envOrDefault("CONFLUENCE_PARENT_PAGE_ID", "2693234852"),
		BotEmail:     os.Getenv("CONFLUENCE_BOT_EMAIL"),
		Addr:         envOrDefault("ADDR", ":8080"),
		DryRun:       os.Getenv("CONFLUENCE_DRY_RUN") == "true",
	}

	token, err := loadConfluenceToken(ctx)
	if err != nil {
		return config{}, err
	}
	cfg.APIToken = token
	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
