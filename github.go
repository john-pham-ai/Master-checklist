package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type githubTag struct {
	Name string `json:"name"`
}

// fetchGithubTags lists tag names for owner/repo via the GitHub REST API,
// using a personal access token for auth (the repo is private).
func fetchGithubTags(owner, repo, token string) ([]string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s/tags?per_page=100", owner, repo), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github tags request failed: %s", resp.Status)
	}

	var tags []githubTag
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(tags))
	for _, t := range tags {
		names = append(names, t.Name)
	}
	return names, nil
}

// filterTags keeps only tag names containing the given substring (case-insensitive).
func filterTags(tags []string, contains string) []string {
	filtered := make([]string, 0, len(tags))
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), strings.ToLower(contains)) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
