package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// fakeTailscaleJSON is the `tailscale status --json` the default fake
// prints: an empty tailnet, so a blank remote IP means "lookup failed, no
// remote alias". Tests that want the truck present install their own fake.
const fakeTailscaleEmpty = `{"Peer":{}}`

// sshTestEnv returns a config whose SSH pieces point at temp fakes.
func sshTestEnv(t *testing.T) (config, string) {
	t.Helper()
	dir := t.TempDir()
	bin := t.TempDir()
	cfg := config{
		TruckSSHEnabled: true,
		TruckSSHBin:     fakeBin(t, bin, "ssh", fakeSSHScript),
		TruckKeygenBin:  fakeBin(t, bin, "ssh-keygen", fakeKeygenScript),
		TruckTSBin:      fakeBin(t, bin, "tailscale", "printf '%s' '"+fakeTailscaleEmpty+"'\n"),
		TruckSSHTarget:  "applied@192.168.1.11",
		TruckLogRoot:    "/media/hotswap1/frontier",
		TruckSSHDir:     dir,
		VehicleRange:    "801-835",
	}
	return cfg, dir
}

// fakeTailscaleFor returns a fake tailscale binary printing a tailnet with
// the given peers (keyed arbitrarily), so a test controls what lookup sees.
func fakeTailscaleFor(t *testing.T, peers string) string {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s' '{\"Peer\":{" + peers + "}}'\n"
	return fakeBin(t, t.TempDir(), "tailscale", script)
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
	block := truckConfigBlock(cfg, "truck-805", "192.168.1.11", "truck-805")
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
	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
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
		res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
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
		if _, err := setupTruckSSH(context.Background(), cfg, bad, "", ""); err == nil {
			t.Errorf("setupTruckSSH(%q) should have failed", bad)
		}
	}
}

func TestSetupTruckSSHInstallFailureKeepsLocalSteps(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	t.Setenv("FAKE_SSH_EXIT", "255")
	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
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

	res, err := setupTruckSSH(context.Background(), cfg, "805", "sekret", "")
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
	res, err := setupTruckSSH(context.Background(), cfg, "805", "wrongpass", "")
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
	if _, serr := setupTruckSSH(context.Background(), cfg, "805", "", ""); serr != nil {
		t.Fatal(serr)
	}
	t.Setenv("FAKE_SSH_EXIT", "255")
	_, err = fetchTruckRunID(context.Background(), cfg, "805")
	if err == nil || strings.Contains(err.Error(), "Set up SSH") {
		t.Errorf("hint should be gone once the alias exists: %v", err)
	}
}

// The install-failure diagnosis must not claim a key was rejected when the
// truck is simply unreachable (the usual no-cable case).
func TestClassifyInstallDetail(t *testing.T) {
	cases := []struct{ detail, want string }{
		{"ssh: connect to host 192.168.1.11 port 22: Connection timed out", "could not reach the truck"},
		{"ssh: connect to host 192.168.1.11 port 22: Connection refused", "could not reach the truck"},
		{"Permission denied (publickey,password)", "your existing key was not accepted"},
	}
	for _, c := range cases {
		if got := classifyInstallDetail(c.detail); !strings.Contains(got, c.want) {
			t.Errorf("classifyInstallDetail(%q) = %q, want it to mention %q", c.detail, got, c.want)
		}
	}
}

func TestSetupTruckSSHUnreachableIsNotAKeyRejection(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	t.Setenv("FAKE_SSH_EXIT", "255")
	t.Setenv("FAKE_SSH_STDERR", "")
	// The fake ssh prints 'ssh: connect to host 192.168.1.11 port 22: Connection timed out'.
	fake := strings.Replace(fakeSSHScript, `cat >/dev/null`,
		`cat >/dev/null; echo 'ssh: connect to host 192.168.1.11 port 22: Connection timed out' >&2`, 1)
	cfg.TruckSSHBin = fakeBin(t, t.TempDir(), "ssh", fake)
	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.KeyInstalled {
		t.Fatal("install reported success")
	}
	if !strings.Contains(res.InstallDetail, "could not reach the truck") {
		t.Errorf("unreachable truck misdiagnosed: %q", res.InstallDetail)
	}
	if strings.Contains(res.InstallDetail, "key was not accepted") {
		t.Errorf("should not claim the key was rejected: %q", res.InstallDetail)
	}
}

func TestValidRemoteIP(t *testing.T) {
	for _, good := range []string{"100.65.197.86", "192.168.1.11", "10.0.0.1", "255.255.255.255"} {
		if !validRemoteIP(good) {
			t.Errorf("validRemoteIP(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{"", "100.65.197", "100.65.197.256", "100.65.197.086",
		"100.65.197.86; rm -rf /", "truck-805.remote", "ssh://100.65.197.86", "999.1.1.1", "1.2.3.4.5"} {
		if validRemoteIP(bad) {
			t.Errorf("validRemoteIP(%q) = true, want false", bad)
		}
	}
}

func TestSetupTruckSSHAddsRemoteAlias(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "100.65.197.86")
	if err != nil {
		t.Fatalf("setupTruckSSH: %v", err)
	}
	if res.RemoteAlias != "truck-805-remote" || res.RemoteHost != "100.65.197.86" {
		t.Errorf("remote fields wrong: %+v", res)
	}
	if res.RemoteLogin != "applied@100.65.197.86" {
		t.Errorf("remote login = %q", res.RemoteLogin)
	}
	conf := readTruckFile(t, dir, "config")
	if !strings.Contains(conf, "Host truck-805-remote") || !strings.Contains(conf, "HostName 100.65.197.86") {
		t.Errorf("remote block missing:\n%s", conf)
	}
	// Same identity serves both aliases.
	if got := strings.Count(conf, "IdentityFile "+filepath.Join(dir, "truck-805")); got != 2 {
		t.Errorf("both blocks should share the per-truck identity, found %d: \n%s", got, conf)
	}

	// Idempotent: a second run with the same IP adds nothing.
	res2, err := setupTruckSSH(context.Background(), cfg, "805", "", "100.65.197.86")
	if err != nil {
		t.Fatal(err)
	}
	if res2.RemoteConfigAdded || res2.ConfigAdded || res2.KeyCreated {
		t.Errorf("second run should reuse everything: %+v", res2)
	}
	if strings.Count(readTruckFile(t, dir, "config"), "Host truck-805-remote") != 1 {
		t.Error("remote block duplicated on rerun")
	}
}

func TestSetupTruckSSHWithoutRemoteIPHasNoRemoteAlias(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteAlias != "" {
		t.Errorf("no remote IP given but remote alias = %q", res.RemoteAlias)
	}
	if strings.Contains(readTruckFile(t, dir, "config"), "-remote") {
		t.Error("remote block should not exist")
	}
}

func TestSetupTruckSSHRejectsBadRemoteIP(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	if _, err := setupTruckSSH(context.Background(), cfg, "805", "", "100.65.197.86; rm"); err == nil {
		t.Error("bad remote IP accepted")
	}
}

func TestTruckSSHSetupHandlerRemoteIP(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	handler := makeTruckSSHSetupHandler(cfg)
	post := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/truck/ssh_setup", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	// A remote IP adds the remote alias to the response and config.
	rec := post(url.Values{"vehicle": {"805"}, "remote_ip": {"100.65.197.86"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var res truckSetupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.RemoteAlias != "truck-805-remote" || res.RemoteLogin != "applied@100.65.197.86" {
		t.Errorf("remote fields: %+v", res)
	}

	// A malformed remote IP is rejected before anything is written.
	rec = post(url.Values{"vehicle": {"806"}, "remote_ip": {"100.65.197.86; rm"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad remote ip status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "plain IPv4 address") {
		t.Errorf("error text: %q", rec.Body.String())
	}
	if strings.Contains(readTruckFile(t, cfg.TruckSSHDir, "config"), "truck-806") {
		t.Error("rejected request should not have written a config block")
	}
}

func TestLookupTailscaleTruck(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	peer805 := `"n1":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.65.197.86","fd7a:115c:a1e0::dead:beef"],"Online":true},` +
		`"n2":{"HostName":"truck-805-backuppc","TailscaleIPs":["100.119.201.57"],"Online":true},` +
		`"n3":{"HostName":"someone-laptop","TailscaleIPs":["100.1.2.3"],"Online":true}`

	t.Run("found, online, backup pc ignored, IPv6 skipped", func(t *testing.T) {
		cfg.TruckTSBin = fakeTailscaleFor(t, peer805)
		ip, online, err := lookupTailscaleTruck(context.Background(), cfg, "805")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if ip != "100.65.197.86" || !online {
			t.Errorf("lookup = (%q, %v)", ip, online)
		}
	})

	t.Run("duplicates collapse, online twin wins", func(t *testing.T) {
		// Same node listed twice: same IP (offline copy) and a second IP —
		// the online one must win regardless of order.
		peers := `"a":{"HostName":"truck-807-primarypc","TailscaleIPs":["100.9.9.1"],"Online":false},` +
			`"b":{"HostName":"truck-807-primarypc","TailscaleIPs":["100.9.9.1"],"Online":true},` +
			`"c":{"HostName":"truck-807-primarypc","TailscaleIPs":["100.9.9.2"],"Online":false}`
		cfg.TruckTSBin = fakeTailscaleFor(t, peers)
		ip, online, err := lookupTailscaleTruck(context.Background(), cfg, "807")
		if err != nil || ip != "100.9.9.1" || !online {
			t.Errorf("lookup = (%q, %v, %v)", ip, online, err)
		}
	})

	t.Run("offline truck still resolves, reported offline", func(t *testing.T) {
		peers := `"a":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.65.197.86"],"Online":false}`
		cfg.TruckTSBin = fakeTailscaleFor(t, peers)
		ip, online, err := lookupTailscaleTruck(context.Background(), cfg, "805")
		if err != nil || ip != "100.65.197.86" || online {
			t.Errorf("lookup = (%q, %v, %v)", ip, online, err)
		}
	})

	t.Run("not on the tailnet", func(t *testing.T) {
		cfg.TruckTSBin = fakeTailscaleFor(t, peer805)
		_, _, err := lookupTailscaleTruck(context.Background(), cfg, "835")
		if err == nil || !strings.Contains(err.Error(), `no machine named "truck-835-primarypc"`) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("several online IPs is an ambiguity error", func(t *testing.T) {
		peers := `"a":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.1.1.1"],"Online":true},` +
			`"b":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.2.2.2"],"Online":true}`
		cfg.TruckTSBin = fakeTailscaleFor(t, peers)
		_, _, err := lookupTailscaleTruck(context.Background(), cfg, "805")
		if err == nil || !strings.Contains(err.Error(), "enter the right IP manually") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("missing binary is a friendly error", func(t *testing.T) {
		cfg.TruckTSBin = "definitely-not-tailscale"
		_, _, err := lookupTailscaleTruck(context.Background(), cfg, "805")
		if err == nil || !strings.Contains(err.Error(), "could not ask Tailscale") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("garbage output is a friendly error", func(t *testing.T) {
		cfg.TruckTSBin = fakeBin(t, t.TempDir(), "tailscale", "printf 'not json'\n")
		_, _, err := lookupTailscaleTruck(context.Background(), cfg, "805")
		if err == nil || !strings.Contains(err.Error(), "could not parse") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestSetupTruckSSHAutoLookupFromTailscale(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	cfg.TruckTSBin = fakeTailscaleFor(t,
		`"n1":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.65.197.86"],"Online":true}`)

	// Blank remote IP: the IP comes from Tailscale, no manual input.
	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
	if err != nil {
		t.Fatalf("setupTruckSSH: %v", err)
	}
	if res.RemoteSource != "tailscale" || res.RemoteHost != "100.65.197.86" || res.RemoteLogin != "applied@100.65.197.86" {
		t.Errorf("auto lookup fields: %+v", res)
	}
	if !strings.Contains(readTruckFile(t, dir, "config"), "Host truck-805-remote") {
		t.Error("remote block missing")
	}
	if res.RemoteNote != "" {
		t.Errorf("unexpected note: %q", res.RemoteNote)
	}

	// Manual IP overrides the lookup, and no tailscale call is needed.
	cfg2, _ := sshTestEnv(t)
	cfg2.TruckTSBin = "definitely-not-tailscale"
	res2, err := setupTruckSSH(context.Background(), cfg2, "805", "", "10.1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if res2.RemoteSource != "manual" || res2.RemoteHost != "10.1.2.3" {
		t.Errorf("manual override: %+v", res2)
	}
}

func TestSetupTruckSSHLookupFailureOnlyCostsTheRemoteAlias(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	cfg.TruckTSBin = "definitely-not-tailscale" // lookup fails

	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
	if err != nil {
		t.Fatalf("a failed lookup must not fail the whole setup: %v", err)
	}
	if res.RemoteAlias != "" {
		t.Errorf("no remote alias expected: %+v", res)
	}
	if !strings.Contains(res.RemoteNote, "Remote login was skipped") || !strings.Contains(res.RemoteNote, "tailscale") {
		t.Errorf("note should explain the skip: %q", res.RemoteNote)
	}
	if !strings.Contains(readTruckFile(t, dir, "config"), "Host truck-805") {
		t.Error("local block missing")
	}
}

func TestSetupTruckSSHOfflineTailscaleTruckStillConfigures(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	cfg.TruckTSBin = fakeTailscaleFor(t,
		`"n1":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.65.197.86"],"Online":false}`)

	res, err := setupTruckSSH(context.Background(), cfg, "805", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteHost != "100.65.197.86" || !strings.Contains(res.RemoteNote, "offline") {
		t.Errorf("offline truck should still be configured with a note: %+v", res)
	}
}

func TestTruckSSHSetupHandlerAutoLookup(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	cfg.TruckTSBin = fakeTailscaleFor(t,
		`"n1":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.65.197.86"],"Online":true}`)
	handler := makeTruckSSHSetupHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/truck/ssh_setup", strings.NewReader("vehicle=805"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var res truckSetupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.RemoteSource != "tailscale" || res.RemoteAlias != "truck-805-remote" {
		t.Errorf("handler result: %+v", res)
	}
}

func TestTruckIPLookupHandler(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	cfg.TruckTSBin = fakeTailscaleFor(t,
		`"n1":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.65.197.86"],"Online":true}`)
	handler := makeTruckIPLookupHandler(cfg)

	t.Run("found online", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/truck/ssh_lookup?vehicle=805", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
		}
		var res struct {
			IP     string `json:"ip"`
			Online bool   `json:"online"`
			Host   string `json:"host"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.IP != "100.65.197.86" || !res.Online || res.Host != "truck-805-primarypc" {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("not on the tailnet is 200 with the reason, not an error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/truck/ssh_lookup?vehicle=804", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var res struct {
			IP    string `json:"ip"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.IP != "" || !strings.Contains(res.Error, "truck-804-primarypc") {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("bad vehicle is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/truck/ssh_lookup?vehicle=9999", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("POST is 405", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodPost, "/api/truck/ssh_lookup", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("disabled and not dry run is 503 like the setup", func(t *testing.T) {
		cfg := cfg
		cfg.TruckSSHEnabled = false
		cfg.DryRun = false
		rec := httptest.NewRecorder()
		makeTruckIPLookupHandler(cfg)(rec, httptest.NewRequest(http.MethodGet, "/api/truck/ssh_lookup?vehicle=805", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestSetupTruckSSHConcurrentIsSerialized(t *testing.T) {
	cfg, dir := sshTestEnv(t)
	cfg.TruckTSBin = fakeTailscaleFor(t,
		`"n1":{"HostName":"truck-805-primarypc","TailscaleIPs":["100.65.197.86"],"Online":true}`)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Keygen races abort; config races duplicate. With the lock
			// neither happens: every run returns nil.
			if _, err := setupTruckSSH(context.Background(), cfg, "805", "", ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent setup: %v", err)
	}
	if n := strings.Count(readTruckFile(t, dir, "config"), "Host truck-805\n"); n != 1 {
		t.Errorf("local Host blocks = %d, want 1", n)
	}
	if n := strings.Count(readTruckFile(t, dir, "config"), "Host truck-805-remote\n"); n != 1 {
		t.Errorf("remote Host blocks = %d, want 1", n)
	}
}

func TestTruckSSHSetupHandlerAutofilledSource(t *testing.T) {
	cfg, _ := sshTestEnv(t)
	cfg.TruckTSBin = "definitely-not-tailscale" // autofill happened earlier; no lookup now
	handler := makeTruckSSHSetupHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/truck/ssh_setup",
		strings.NewReader("vehicle=805&remote_ip=10.5.5.5&remote_ip_source=tailscale"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var res truckSetupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.RemoteSource != "tailscale" || res.RemoteHost != "10.5.5.5" {
		t.Errorf("autofilled IP must be used and labeled tailscale: %+v", res)
	}
}
