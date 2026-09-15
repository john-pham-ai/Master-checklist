package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVehicleAllowed(t *testing.T) {
	cases := []struct {
		id      string
		spec    string
		allowed bool
	}{
		{"805", "801-835", true},
		{"801", "801-835", true},
		{"835", "801-835", true},
		{"836", "801-835", false},
		{"800", "801-835", false},
		{"900", "801-835,900", true},
		{"", "801-835", false},
		{"abc", "801-835", false},
		{"80a", "801-835", false},
		{"805; rm -rf /", "801-835", false},
		{"-1", "801-835", false},
		{"0805", "801-835", false},
	}
	for _, c := range cases {
		if got := vehicleAllowed(c.id, c.spec); got != c.allowed {
			t.Errorf("vehicleAllowed(%q, %q) = %v, want %v", c.id, c.spec, got, c.allowed)
		}
	}
}

func TestTruckRemoteScriptIsFixed(t *testing.T) {
	s := truckRemoteScript("/media/hotswap1/frontier")
	if !strings.Contains(s, "/media/hotswap1/frontier/truck-") {
		t.Error("script does not reference the log root")
	}
	if !strings.Contains(s, "date +%Y/%m/%d") {
		t.Error("script does not look at the truck's own today directory")
	}
	// The vehicle is derived from the hostname, never taken from the request.
	if strings.Contains(s, "{VEHICLE}") {
		t.Error("script still has an unfilled placeholder")
	}
}

func TestParseTruckOutput(t *testing.T) {
	const root = "/media/hotswap1/frontier/truck-805/2026/09/15/2026-09-15_14-48-57_truck-805"

	t.Run("today's run", func(t *testing.T) {
		out := "HOSTNAME\ttruck-805-primarypc\nTODAY\t" + root + "\nLATEST\t" + root + "\n"
		info, err := parseTruckOutput(out, "805")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.RunID != "2026-09-15_14-48-57_truck-805" {
			t.Errorf("RunID = %q", info.RunID)
		}
		if info.Path != root {
			t.Errorf("Path = %q", info.Path)
		}
		if info.Date != "2026/09/15" {
			t.Errorf("Date = %q", info.Date)
		}
		if info.Vehicle != "805" {
			t.Errorf("Vehicle = %q", info.Vehicle)
		}
		if info.Warning != "" {
			t.Errorf("unexpected warning %q", info.Warning)
		}
	})

	t.Run("no run today falls back with warning", func(t *testing.T) {
		old := "/media/hotswap1/frontier/truck-805/2026/09/14/2026-09-14_09-00-00_truck-805"
		out := "HOSTNAME\ttruck-805-primarypc\nTODAY\t\nLATEST\t" + old + "\n"
		info, err := parseTruckOutput(out, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.RunID != "2026-09-14_09-00-00_truck-805" {
			t.Errorf("RunID = %q", info.RunID)
		}
		if info.Date != "2026/09/14" {
			t.Errorf("Date = %q", info.Date)
		}
		if !strings.Contains(info.Warning, "earlier day") {
			t.Errorf("missing fallback warning, got %q", info.Warning)
		}
	})

	t.Run("requested vehicle mismatch warns", func(t *testing.T) {
		out := "HOSTNAME\ttruck-805-primarypc\nTODAY\t" + root + "\nLATEST\t" + root + "\n"
		info, err := parseTruckOutput(out, "812")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(info.Warning, "not 812") {
			t.Errorf("missing mismatch warning, got %q", info.Warning)
		}
	})

	t.Run("no hostname is an error", func(t *testing.T) {
		if _, err := parseTruckOutput("TODAY\t/x\n", ""); err == nil {
			t.Error("expected an error for output without HOSTNAME")
		}
	})

	t.Run("NOVEHICLE is an error", func(t *testing.T) {
		if _, err := parseTruckOutput("HOSTNAME\tlaptop\nNOVEHICLE\n", ""); err == nil {
			t.Error("expected an error for a non-truck host")
		}
	})

	t.Run("no runs found is an error", func(t *testing.T) {
		out := "HOSTNAME\ttruck-805-primarypc\nTODAY\t\nLATEST\t\n"
		if _, err := parseTruckOutput(out, ""); err == nil {
			t.Error("expected an error when no run directories exist")
		}
	})
}

// fakeSSH writes a script that plays the part of the ssh binary: it consumes
// the script on stdin and prints the given canned stdout.
func fakeSSH(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\n" + stdout
	if exitCode != 0 {
		script += "echo 'ssh: connect refused' >&2\nexit " + itoa(exitCode) + "\n"
	} else {
		script += "cat >/dev/null\n"
	}
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func itoa(n int) string { return fmt.Sprint(n) }

func truckTestConfig(sshBin string) config {
	return config{
		TruckSSHEnabled: true,
		TruckSSHBin:     sshBin,
		TruckSSHTarget:  "applied@192.168.1.11",
		TruckLogRoot:    "/media/hotswap1/frontier",
		VehicleRange:    "801-835",
	}
}

func TestTruckRunIDHandlerWithFakeSSH(t *testing.T) {
	const root = "/media/hotswap1/frontier/truck-805/2026/09/15/2026-09-15_14-48-57_truck-805"
	out := "printf 'HOSTNAME\\ttruck-805-primarypc\\nTODAY\\t" + root + "\\nLATEST\\t" + root + "\\n'\n"
	cfg := truckTestConfig(fakeSSH(t, out, 0))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/truck/run_id?vehicle=805", nil)
	makeTruckRunIDHandler(cfg)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %q", rec.Code, rec.Body.String())
	}
	var info truckRunInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("bad JSON: %v (%q)", err, rec.Body.String())
	}
	if info.RunID != "2026-09-15_14-48-57_truck-805" {
		t.Errorf("RunID = %q", info.RunID)
	}
	if info.Path != root {
		t.Errorf("Path = %q", info.Path)
	}
	if info.Host != "truck-805-primarypc" || info.Vehicle != "805" {
		t.Errorf("Host/Vehicle = %q/%q", info.Host, info.Vehicle)
	}

	// Without a vehicle parameter, the truck is derived from the hostname.
	rec = httptest.NewRecorder()
	makeTruckRunIDHandler(cfg)(rec, httptest.NewRequest("GET", "/api/truck/run_id", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no-vehicle status = %d, body %q", rec.Code, rec.Body.String())
	}
}

func TestTruckRunIDHandlerErrors(t *testing.T) {
	// Invalid vehicle is rejected before anything is executed.
	cfg := truckTestConfig(fakeSSH(t, "printf 'HOSTNAME\\ttruck-805-primarypc\\n'\n", 0))
	rec := httptest.NewRecorder()
	makeTruckRunIDHandler(cfg)(rec, httptest.NewRequest("GET", "/api/truck/run_id?vehicle=805%3B%20rm", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid vehicle status = %d, want 400", rec.Code)
	}

	// ssh-level failure (exit 255) becomes a readable 502.
	cfg = truckTestConfig(fakeSSH(t, "", 255))
	rec = httptest.NewRecorder()
	makeTruckRunIDHandler(cfg)(rec, httptest.NewRequest("GET", "/api/truck/run_id?vehicle=805", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("ssh-failure status = %d, want 502", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if !strings.Contains(body["error"], "could not SSH") {
		t.Errorf("error text not helpful: %q", body["error"])
	}

	// Disabled: 404 (as on Cloud Run, where the feature is off).
	cfg = truckTestConfig("ssh")
	cfg.TruckSSHEnabled = false
	rec = httptest.NewRecorder()
	makeTruckRunIDHandler(cfg)(rec, httptest.NewRequest("GET", "/api/truck/run_id?vehicle=805", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("disabled status = %d, want 404", rec.Code)
	}
}

func TestTruckRunIDHandlerDryRunReturnsFake(t *testing.T) {
	cfg := truckTestConfig("ssh")
	cfg.TruckSSHEnabled = false
	cfg.DryRun = true
	rec := httptest.NewRecorder()
	makeTruckRunIDHandler(cfg)(rec, httptest.NewRequest("GET", "/api/truck/run_id?vehicle=810", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %q", rec.Code, rec.Body.String())
	}
	var info truckRunInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("bad JSON: %v (%q)", err, rec.Body.String())
	}
	if info.Vehicle != "810" || !strings.Contains(info.Path, "/truck-810/") || !strings.Contains(info.RunID, "_truck-810") {
		t.Errorf("dry-run fake wrong: %+v", info)
	}
}
