package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/oauth2/google"
)

// engineers_snapshot.json is a point-in-time export of the Okta/Google group
// memberships used to suggest Test Engineer names. Regenerate it with
// scripts/refresh_engineers.sh. It is only a fallback: at runtime the app first
// tries the Cloud Identity API, which works as soon as the app's service
// account is granted permission to read the groups.
//
//go:embed engineers_snapshot.json
var engineersSnapshotJSON []byte

// defaultEngineerGroups are the access groups whose members are offered as
// Test Engineer suggestions. okta-team-applied-fte (all ~1500 FTEs) is
// deliberately excluded; override with ENGINEER_GROUPS if that changes.
// Keep in sync with allowed_usergroups in project.toml and the default in
// scripts/refresh_engineers.sh.
const defaultEngineerGroups = "okta-team-vehicle-testing@applied.co,okta-ext-frontier@applied.co,okta-ext-vehicle_operators-jp@applied.co"

const engineerCacheTTL = time.Hour

const cloudIdentityScope = "https://www.googleapis.com/auth/cloud-identity.groups.readonly"

type engineer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type engineerSnapshot struct {
	Generated string              `json:"generated"`
	Groups    map[string][]string `json:"groups"`
}

// engineerSource resolves the members of the configured groups, preferring a
// live Cloud Identity lookup and falling back per group to the embedded
// snapshot. Results are cached for engineerCacheTTL.
type engineerSource struct {
	groups   []string
	dryRun   bool
	snapshot engineerSnapshot

	mu        sync.Mutex
	client    *http.Client
	cache     []engineer
	fetchedAt time.Time
	warned    map[string]bool
}

var dryRunEngineers = []engineer{
	{Name: "Ada Lovelace", Email: "ada.lovelace@applied.co"},
	{Name: "Grace Hopper", Email: "grace.hopper@ext.applied.co"},
	{Name: "John Pham", Email: "john.pham@applied.co"},
}

func newEngineerSource(groupsCSV string, dryRun bool) *engineerSource {
	s := &engineerSource{dryRun: dryRun, warned: map[string]bool{}}
	for _, g := range strings.Split(groupsCSV, ",") {
		if g = strings.TrimSpace(g); g != "" {
			s.groups = append(s.groups, g)
		}
	}
	if err := json.Unmarshal(engineersSnapshotJSON, &s.snapshot); err != nil {
		log.Printf("engineers: embedded snapshot is invalid: %v", err)
	}
	return s
}

// Get returns the deduplicated, name-sorted list of engineers across all groups.
func (s *engineerSource) Get(ctx context.Context) []engineer {
	if s.dryRun {
		return dryRunEngineers
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && time.Since(s.fetchedAt) < engineerCacheTTL {
		return s.cache
	}

	seen := map[string]bool{}
	for _, g := range s.groups {
		members, err := s.listMembers(ctx, g)
		if err != nil {
			if !s.warned[g] {
				log.Printf("engineers: live lookup of %s failed (%v); using embedded snapshot (%d entries, generated %s)",
					g, err, len(s.snapshot.Groups[g]), s.snapshot.Generated)
				s.warned[g] = true
			}
			members = s.snapshot.Groups[g]
		}
		for _, e := range members {
			seen[strings.ToLower(strings.TrimSpace(e))] = true
		}
	}

	list := make([]engineer, 0, len(seen))
	for e := range seen {
		if e == "" {
			continue
		}
		list = append(list, engineer{Name: displayNameFromEmail(e), Email: e})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].Email < list[j].Email
	})

	s.cache = list
	s.fetchedAt = time.Now()
	return list
}

// httpClient lazily builds an authenticated client using Application Default
// Credentials (the Cloud Run service account in production). It is created
// with a background context so token refreshes are not tied to one request.
func (s *engineerSource) httpClient() (*http.Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	client, err := google.DefaultClient(context.Background(), cloudIdentityScope)
	if err != nil {
		return nil, fmt.Errorf("google.DefaultClient: %w", err)
	}
	client.Timeout = 20 * time.Second
	s.client = client
	return client, nil
}

// listMembers returns member emails of groupEmail via the Cloud Identity API.
func (s *engineerSource) listMembers(ctx context.Context, groupEmail string) ([]string, error) {
	client, err := s.httpClient()
	if err != nil {
		return nil, err
	}

	var lookup struct {
		Name string `json:"name"` // e.g. "groups/04k668n31pvq6zy"
	}
	if err := getJSON(ctx, client, "https://cloudidentity.googleapis.com/v1/groups:lookup?groupKey.id="+url.QueryEscape(groupEmail), &lookup); err != nil {
		return nil, err
	}

	var emails []string
	pageToken := ""
	for {
		u := fmt.Sprintf("https://cloudidentity.googleapis.com/v1/%s/memberships?view=BASIC&pageSize=1000", lookup.Name)
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		var page struct {
			Memberships []struct {
				PreferredMemberKey struct {
					ID string `json:"id"`
				} `json:"preferredMemberKey"`
			} `json:"memberships"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := getJSON(ctx, client, u, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Memberships {
			if id := m.PreferredMemberKey.ID; strings.Contains(id, "@") {
				emails = append(emails, id)
			}
		}
		if page.NextPageToken == "" {
			return emails, nil
		}
		pageToken = page.NextPageToken
	}
}

func getJSON(ctx context.Context, client *http.Client, u string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s: %s", u, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// displayNameFromEmail turns "brandon.moyer@ext.applied.co" into "Brandon Moyer".
// Dots and underscores separate name parts; hyphenated parts keep the hyphen
// with each side capitalised ("mary-jane.smith" -> "Mary-Jane Smith").
func displayNameFromEmail(email string) string {
	local := email
	if i := strings.Index(email, "@"); i >= 0 {
		local = email[:i]
	}
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '_' })
	for i, p := range parts {
		parts[i] = capitalizeHyphenated(p)
	}
	return strings.Join(parts, " ")
}

func capitalizeHyphenated(s string) string {
	segs := strings.Split(s, "-")
	for i, seg := range segs {
		if seg == "" {
			continue
		}
		r := []rune(strings.ToLower(seg))
		r[0] = unicode.ToUpper(r[0])
		segs[i] = string(r)
	}
	return strings.Join(segs, "-")
}

// currentUserEmail returns the signed-in user's email from the IAP header
// ("accounts.google.com:user@applied.co"), or "" when not behind IAP (local dev).
func currentUserEmail(r *http.Request) string {
	v := r.Header.Get("X-Goog-Authenticated-User-Email")
	return strings.TrimPrefix(v, "accounts.google.com:")
}

// currentEngineerName is the default value for the Test Engineer field.
func currentEngineerName(r *http.Request) string {
	email := currentUserEmail(r)
	if email == "" {
		return ""
	}
	return displayNameFromEmail(email)
}

func makeEngineersHandler(src *engineerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, max-age=300")
		json.NewEncoder(w).Encode(src.Get(r.Context()))
	}
}
