package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes an executable stand-in (keygen/ssh/askpass) into dir.
func fakeBin(t *testing.T, dir, name, script string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeKeygen mimics ssh-keygen enough for setupTruckSSH: -f <path> is the
// 9th argument in the fixed invocation (-q -t ed25519 -N "" -C c -f path).
// Like the real thing, the private key is 0600.
const fakeKeygenScript = `umask 077
echo FAKE-PRIVATE-KEY > "$9"
umask 022
echo ssh-ed25519 FAKEPUBKEY truck-comment > "$9.pub"
`

// fakeSSHScript records its command line (FAKE_SSH_LOG) and the askpass env
// (FAKE_SSH_ENV_LOG), answers the fetch script's fixed output, and exits
// with FAKE_SSH_EXIT — unless a password is being offered, when exit 0
// models a successful password login.
const fakeSSHScript = `[ -n "$FAKE_SSH_LOG" ] && printf '%s\n' "$*" >> "$FAKE_SSH_LOG"
[ -n "$FAKE_SSH_ENV_LOG" ] && env | grep -E 'SSH_ASKPASS_REQUIRE|TRUCK_SSH_PASSWORD' >> "$FAKE_SSH_ENV_LOG" 2>/dev/null
cat >/dev/null
printf 'HOSTNAME\ttruck-805-primarypc\n'
printf 'TODAY\t/media/hotswap1/frontier/truck-805/2026/09/15/2026-09-15_14-48-57_truck-805\n'
printf 'LATEST\t/media/hotswap1/frontier/truck-805/2026/09/15/2026-09-15_14-48-57_truck-805\n'
if [ -n "$TRUCK_SSH_PASSWORD" ] && [ -z "$FAKE_SSH_REJECT_PASSWORD" ]; then exit 0; fi
exit "${FAKE_SSH_EXIT:-0}"
`

// sshTestEnv returns a config whose SSH pieces point at temp fakes.
func sshTestEnv(t *testing.T) (config, string) {
	t.Helper()
	dir := t.TempDir()
	bin := t.TempDir()
	cfg := config{
		TruckSSHEnabled: true,
		TruckSSHBin:     fakeBin(t, bin, "ssh", fakeSSHScript),
		TruckKeygenBin:  fakeBin(t, bin, "ssh-keygen", fakeKeygenScript),
		TruckSSHTarget:  "applied@192.168.1.11",
		TruckLogRoot:    "/media/hotswap1/frontier",
		TruckSSHDir:     dir,
		VehicleRange:    "801-835",
	}
	return cfg, dir
}

func readTruckFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}

func TestTruckConfigBlock(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	block := truckConfigBlock(cfg, "805")
	for _, want := range []string{
		"Host truck-805\n",
		"    HostName 192.168.1.11\n",
		"    User applied\n",
		"    IdentityFile " + filepath.Join(dir, "truck-805") + "\n",
		"    IdentitiesOnly yes\n",
		"    UserKnownHostsFile " + filepath.Join(dir, "known_hosts.d", "truck-805") + "\n",
		"    StrictHostKeyChecking accept-new\n",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("config block missing %q:\n%s", want, block)
		}
	}
}

func TestSetupTruckSSHCreatesIdentityAndConfig(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	res, err := setupTruckSSH(context.Background(), cfg, "805", "")
	if err != nil {
		t.Fatalf("setupTruckSSH: %v", err)
	}
	if !res.KeyCreated || !res.ConfigAdded || !res.KeyInstalled {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Alias != "truck-805" || res.Vehicle != "805" {
		t.Errorf("name not forced to the truck number: %+v", res)
	}
	if res.PublicKey != "ssh-ed25519 FAKEPUBKEY truck-comment" {
		t.Errorf("public key = %q", res.PublicKey)
	}
	// Identity on disk, 0600 private.
	key := readTruckFile(t, filepath.Join(dir, "truck-805"))
	if !strings.Contains(key, "FAKE-PRIVATE-KEY") {
		t.Errorf("private key content: %q", key)
	}
	if fi, err := os.Stat(filepath.Join(dir, "truck-805")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %v, want 0600 (%v)", fi, err)
	}
	// Config block appended with the forced name.
	conf := readTruckFile(t, dir, "config")
	if !strings.Contains(conf, "Host truck-805") {
		t.Errorf("config missing alias:\n%s", conf)
	}

	// The fetch now resolves to the alias, not the raw address.
	log := filepath.Join(t.TempDir(), "sshlog")
	t.Setenv("FAKE_SSH_LOG", log)
	if _, err := fetchTruckRunID(context.Background(), cfg, "805"); err != nil {
		t.Fatalf("fetch after setup: %v", err)
	}
	if called := readTruckFile(t, log); !strings.Contains(called, "truck-805") {
		t.Errorf("fetch did not use the alias, called: %q", called)
	}
}

func TestSetupTruckSSHIsIdempotent(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	for i := 0; i < 2; i++ {
		res, err := setupTruckSSH(context.Background(), cfg, "805", "")
		if err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if i == 0 && (!res.KeyCreated || !res.ConfigAdded) {
			t.Fatalf("first run should create: %+v", res)
		}
		if i == 1 && (res.KeyCreated || res.ConfigAdded) {
			t.Fatalf("second run should reuse: %+v", res)
		}
	}
	// Config contains exactly one Host truck-805 block.
	conf := readTruckFile(t, cfg.TruckSSHDir, "config")
	if n := strings.Count(conf, "Host truck-805"); n != 1 {
		t.Errorf("config has %d blocks for truck-805:\n%s", n, conf)
	}
}

func TestSetupTruckSSHForcesVehicleNumber(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	for _, bad := range []string{"", "805; rm -rf /", "abc", "836", "80a"} {
		if _, err := setupTruckSSH(context.Background(), cfg, bad, ""); err == nil {
			t.Errorf("setupTruckSSH(%q) should have failed", bad)
		}
	}
}

func TestSetupTruckSSHInstallFailureKeepsLocalSteps(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	t.Setenv("FAKE_SSH_EXIT", "255")
	res, err := setupTruckSSH(context.Background(), cfg, "805", "")
	if err != nil {
		t.Fatalf("install failure should not fail the whole setup: %v", err)
	}
	if res.KeyInstalled {
		t.Fatalf("install reported success: %+v", res)
	}
	if !res.KeyCreated || !res.ConfigAdded {
		t.Errorf("local steps missing: %+v", res)
	}
	if !strings.Contains(res.NextStep, "ssh-copy-id") || !strings.Contains(res.NextStep, "truck-805.pub") {
		t.Errorf("next step not a usable copy command: %q", res.NextStep)
	}
	// The identity is still there for the manual install.
	if !strings.Contains(readTruckFile(t, dir, "truck-805.pub"), "FAKEPUBKEY") {
		t.Error("public key missing after failed install")
	}
}

func TestSetupTruckSSHPasswordGoesThroughAskpass(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	envLog := filepath.Join(t.TempDir(), "envlog")
	t.Setenv("FAKE_SSH_ENV_LOG", envLog)
	// Attempt 1 (existing key) fails: exit 255 without a password on offer.
	t.Setenv("FAKE_SSH_EXIT", "255")

	res, err := setupTruckSSH(context.Background(), cfg, "805", "sekret")
	if err != nil {
		t.Fatalf("setupTruckSSH: %v", err)
	}
	if !res.KeyInstalled || !strings.Contains(res.InstallDetail, "password") {
		t.Fatalf("password path not taken: %+v", res)
	}
	// The askpass machinery reached ssh, and the password travelled in the
	// environment of the ssh process, never on a command line.
	logged := readTruckFile(t, envLog)
	if !strings.Contains(logged, "SSH_ASKPASS_REQUIRE=force") {
		t.Errorf("SSH_ASKPASS_REQUIRE not set for ssh:\n%s", logged)
	}
	if !strings.Contains(logged, "TRUCK_SSH_PASSWORD=sekret") {
		t.Errorf("password not passed via env:\n%s", logged)
	}
}

func TestSetupTruckSSHPasswordNotAcceptedReportsDetail(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	// Both attempts fail: attempt 1 exits 255 (existing key rejected);
	// attempt 2 also 255 (wrong password).
	t.Setenv("FAKE_SSH_EXIT", "255")
	t.Setenv("FAKE_SSH_REJECT_PASSWORD", "1")
	res, err := setupTruckSSH(context.Background(), cfg, "805", "wrongpass")
	if err != nil {
		t.Fatalf("setupTruckSSH: %v", err)
	}
	if res.KeyInstalled {
		t.Fatalf("install reported success: %+v", res)
	}
	if !strings.Contains(res.InstallDetail, "password was not accepted") {
		t.Errorf("detail does not explain the rejection: %q", res.InstallDetail)
	}
}

func TestTruckSSHSetupHandler(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	handler := makeTruckSSHSetupHandler(cfg)
	post := func(vehicle, password string) *httptest.ResponseRecorder {
		form := strings.NewReader("vehicle=" + vehicle + "&password=" + password)
		req := httptest.NewRequest(http.MethodPost, "/api/truck/ssh_setup", form)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	t.Run("method not allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/truck/ssh_setup", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET status = %d, want 405", rec.Code)
		}
	})

	t.Run("bad vehicle is rejected with the naming rule", func(t *testing.T) {
		rec := post("", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body["error"], "named after it") {
			t.Errorf("error text: %q", body["error"])
		}
	})

	t.Run("success", func(t *testing.T) {
		rec := post("805", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
		}
		var res truckSetupResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.Alias != "truck-805" || !res.KeyInstalled {
			t.Errorf("result: %+v", res)
		}
	})

	hosted := cfg
	hosted.TruckSSHEnabled = false
	hosted.DryRun = false
	rec := httptest.NewRecorder()
	form := strings.NewReader("vehicle=805")
	req := httptest.NewRequest(http.MethodPost, "/api/truck/ssh_setup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	makeTruckSSHSetupHandler(hosted)(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("hosted status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "runs locally") {
		t.Errorf("hosted error text: %q", rec.Body.String())
	}
}

func TestTruckSSHSetupHandlerDryRunSkipsInstall(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	cfg.DryRun = true
	cfg.TruckSSHEnabled = false
	rec := httptest.NewRecorder()
	form := strings.NewReader("vehicle=806")
	req := httptest.NewRequest(http.MethodPost, "/api/truck/ssh_setup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	makeTruckSSHSetupHandler(cfg)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var res truckSetupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	// Local steps happen for real (keygen fake), the remote step is skipped.
	if !res.KeyCreated || !res.ConfigAdded {
		t.Errorf("local steps missing in dry run: %+v", res)
	}
	if res.KeyInstalled || !strings.Contains(res.InstallDetail, "dry run") {
		t.Errorf("dry run should skip the install: %+v", res)
	}
}

// The fetch hint points at setup when the key is rejected and no alias exists.
func TestFetchFailureHintsAtSetup(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	t.Setenv("FAKE_SSH_EXIT", "255")
	_, err := fetchTruckRunID(context.Background(), cfg, "805")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Set up SSH for this truck first") {
		t.Errorf("error should hint at setup: %v", err)
	}
	// And after setup (alias exists), the hint disappears.
	if _, serr := setupTruckSSH(context.Background(), cfg, "805", ""); serr != nil {
		t.Fatal(serr)
	}
	t.Setenv("FAKE_SSH_EXIT", "255")
	_, err = fetchTruckRunID(context.Background(), cfg, "805")
	if err == nil || strings.Contains(err.Error(), "Set up SSH") {
		t.Errorf("hint should be gone once the alias exists: %v", err)
	}
}
