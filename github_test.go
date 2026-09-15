package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mkTag(name, sha string) githubTag {
	var t githubTag
	t.Name = name
	t.Commit.SHA = sha
	return t
}

func TestFilterTagsKeepsSHAAndOrder(t *testing.T) {
	tags := []githubTag{
		mkTag("trucking-scheduled-night-2026-08-31", "sha-a"),
		mkTag("trucking-scheduled-night-2026-09-01", "sha-b"),
		mkTag("trucking-candidate-2026-08-26-01", "sha-c"),
		mkTag("verified/trucking-scheduled-night-2026-09-01", "sha-d"),
		mkTag("trucking-scheduled-night-2026-09-01-02", "sha-e"),
	}
	got := filterTags(tags, "scheduled-night")
	want := []string{
		"trucking-scheduled-night-2026-09-01-02",
		"verified/trucking-scheduled-night-2026-09-01",
		"trucking-scheduled-night-2026-09-01",
		"trucking-scheduled-night-2026-08-31",
	}
	if len(got) != len(want) {
		t.Fatalf("filterTags returned %d tags, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("filterTags[%d].Name = %q, want %q", i, got[i].Name, want[i])
		}
	}
	byName := map[string]string{}
	for _, g := range got {
		byName[g.Name] = g.Commit.SHA
	}
	if byName["trucking-scheduled-night-2026-08-31"] != "sha-a" {
		t.Errorf("SHA lost through filterTags: %v", byName)
	}
}

func TestTagsHandlerReturnsNameAndSHA(t *testing.T) {
	cache := &tagCache{cfg: config{DryRun: true}}
	rec := httptest.NewRecorder()
	makeTagsHandler(cache)(rec, httptest.NewRequest("GET", "/api/tags?test_type=master", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var tags []tagInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &tags); err != nil {
		t.Fatalf("response is not [{name,sha}] JSON: %v (%q)", err, rec.Body.String())
	}
	if len(tags) != 3 { // three scheduled-night dry-run sample tags
		t.Fatalf("got %d tags, want 3: %v", len(tags), tags)
	}
	// Newest first, and the SHA matches the name's sample entry.
	want := map[string]string{}
	for _, st := range dryRunSampleTagInfos {
		want[st.Name] = st.Commit.SHA
	}
	for i, tg := range tags {
		if tg.SHA == "" || tg.SHA != want[tg.Name] {
			t.Errorf("tags[%d] = {%s %s}, SHA mismatch", i, tg.Name, tg.SHA)
		}
		if i > 0 && tags[i-1].Name < tg.Name {
			t.Errorf("tags not newest-first: %q before %q", tags[i-1].Name, tg.Name)
		}
	}

	// Candidate test type filters the other family.
	rec = httptest.NewRecorder()
	makeTagsHandler(cache)(rec, httptest.NewRequest("GET", "/api/tags?test_type=candidate", nil))
	tags = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &tags); err != nil {
		t.Fatalf("candidate response: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("got %d candidate tags, want 2: %v", len(tags), tags)
	}
	for _, tg := range tags {
		if tg.SHA == "" {
			t.Errorf("candidate tag %q has no SHA", tg.Name)
		}
	}
}
