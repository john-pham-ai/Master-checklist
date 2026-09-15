package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

type githubTag struct {
	Name string `json:"name"`
	// Commit.SHA is the commit the tag points at; the REST list-tags endpoint
	// reports the peeled commit SHA even for annotated tags.
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// maxTagPages bounds pagination. brain2 has ~450 tags (5 pages of 100) as of
// Sep 2026; the candidate tags live on pages 4-5, so a single page is not enough.
const maxTagPages = 20

// nextLinkRe extracts the rel="next" URL from a GitHub Link header.
var nextLinkRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// githubGetJSON performs an authenticated GET against the GitHub REST API and
// decodes the JSON response into out.
func githubGetJSON(ctx context.Context, token, u string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return fmt.Errorf("github rate limit exhausted (resets at %s)", resp.Header.Get("X-RateLimit-Reset"))
		}
		return fmt.Errorf("github %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchGithubTags lists ALL tags (name + the commit SHA each points at) for
// owner/repo via the GitHub REST API, following Link-header pagination, using
// a personal access token for auth (the repo is private).
func fetchGithubTags(owner, repo, token string) ([]githubTag, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/tags?per_page=100", owner, repo)

	var tags []githubTag
	for page := 1; url != "" && page <= maxTagPages; page++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var pageTags []githubTag
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("github tags request failed (page %d): %s", page, resp.Status)
		}
		err = json.NewDecoder(resp.Body).Decode(&pageTags)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		tags = append(tags, pageTags...)

		url = ""
		if m := nextLinkRe.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			url = m[1]
		}
	}
	return tags, nil
}

// tagDateRe matches the YYYY-MM-DD(-NN) run stamp embedded in brain2 tag names,
// e.g. trucking-scheduled-night-2026-09-01 or trucking-candidate-2026-08-26-01.
var tagDateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:-\d+)?`)

// filterTags keeps only tags whose name contains the given substring
// (case-insensitive), newest first: sorted by the embedded run date
// descending (so trucking-scheduled-night-2026-09-01 precedes
// verified/trucking-scheduled-night-2026-08-04), then by name descending.
func filterTags(tags []githubTag, contains string) []githubTag {
	filtered := make([]githubTag, 0, len(tags))
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t.Name), strings.ToLower(contains)) {
			filtered = append(filtered, t)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		di, dj := tagDateRe.FindString(filtered[i].Name), tagDateRe.FindString(filtered[j].Name)
		if di != dj {
			return di > dj
		}
		return filtered[i].Name > filtered[j].Name
	})
	return filtered
}
