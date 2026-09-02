package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDisplayNameFromEmail(t *testing.T) {
	cases := map[string]string{
		"brandon.moyer@ext.applied.co": "Brandon Moyer",
		"john.pham@applied.co":         "John Pham",
		"mary-jane.smith@applied.co":   "Mary-Jane Smith",
		"jean_luc.picard@applied.co":   "Jean Luc Picard",
		"ADRIAN.CAMPBELL@applied.co":   "Adrian Campbell",
		"single@applied.co":            "Single",
		"":                             "",
	}
	for in, want := range cases {
		if got := displayNameFromEmail(in); got != want {
			t.Errorf("displayNameFromEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCurrentEngineerName(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := currentEngineerName(r); got != "" {
		t.Errorf("no IAP header: got %q, want empty", got)
	}
	r.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:john.pham@applied.co")
	if got := currentEngineerName(r); got != "John Pham" {
		t.Errorf("got %q, want John Pham", got)
	}
}

func TestEmbeddedSnapshotIsUsable(t *testing.T) {
	var snap engineerSnapshot
	if err := json.Unmarshal(engineersSnapshotJSON, &snap); err != nil {
		t.Fatalf("embedded engineers_snapshot.json is invalid JSON: %v", err)
	}
	if snap.Generated == "" {
		t.Error("snapshot has no generated timestamp")
	}
	if len(snap.Groups["okta-team-vehicle-testing@applied.co"]) == 0 {
		t.Error("snapshot has no members for okta-team-vehicle-testing@applied.co; run scripts/refresh_engineers.sh")
	}
}

func TestDryRunEngineers(t *testing.T) {
	src := newEngineerSource(defaultEngineerGroups, true)
	got := src.Get(httptest.NewRequest("GET", "/", nil).Context())
	if len(got) != len(dryRunEngineers) {
		t.Fatalf("dry run returned %d engineers, want %d", len(got), len(dryRunEngineers))
	}
}
