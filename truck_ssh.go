package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Per-truck SSH setup.
//
// Every truck answers on the same address (applied@192.168.1.11 — whichever
// one the laptop is cabled to), so plain `ssh 192.168.1.11` collides: the
// second truck's host key differs from the first's and OpenSSH refuses with
// "REMOTE HOST IDENTIFICATION HAS CHANGED" (fatal under BatchMode). The fix
// is one SSH alias per truck, named after the truck number, with its own
// identity and its own known-hosts file:
//
//	Host truck-805
//	    HostName 192.168.1.11
//	    User applied
//	    IdentityFile ~/.ssh/truck-805
//	    IdentitiesOnly yes
//	    UserKnownHostsFile ~/.ssh/known_hosts.d/truck-805
//	    StrictHostKeyChecking accept-new
//
// When the truck's remote (VPN) IP is supplied, a second alias is added for
// remote login, same identity:
//
//	Host truck-805-remote
//	    HostName 100.65.197.86
//	    ...
//
// setupTruckSSH creates the identity, appends those blocks to ~/.ssh/config
// (idempotently) and installs the public key on the truck. The alias names
// are forced: the only inputs are the vehicle number (validated like the
// fetch) and, optionally, the remote IP (validated as an IPv4 address).
// After setup, fetches for that vehicle SSH to the alias instead of the raw
// address (resolveTruckTarget), and remote login is `ssh truck-805-remote`.

// truckSetupTimeout bounds the key-install SSH round trip.
const truckSetupTimeout = 20 * time.Second

// remoteIPRe accepts a plain IPv4 address — the only thing allowed into the
// remote alias's HostName.
var remoteIPRe = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

// validRemoteIP reports whether s is a well-formed IPv4 address.
func validRemoteIP(s string) bool {
	m := remoteIPRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	for _, octet := range m[1:] {
		if len(octet) > 1 && octet[0] == '0' {
			return false // no leading zeros
		}
		if n, err := strconv.Atoi(octet); err != nil || n > 255 {
			return false
		}
	}
	return true
}

// truckAlias is the SSH Host alias for a vehicle number: "truck-805".
func truckAlias(vehicle string) string { return "truck-" + vehicle }

// truckRemoteAlias is the remote-login alias: "truck-805-remote".
func truckRemoteAlias(vehicle string) string { return truckAlias(vehicle) + "-remote" }

// truckSSHDir is where the identities, config and known_hosts.d live —
// ~/.ssh, or TRUCK_SSH_DIR when overridden (tests point it at a temp dir).
func truckSSHDir(cfg config) string {
	if cfg.TruckSSHDir != "" {
		return cfg.TruckSSHDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ssh"
	}
	return filepath.Join(home, ".ssh")
}

// splitTarget splits "applied@192.168.1.11" into user and host. A target
// without a user yields user "".
func splitTarget(target string) (user, host string) {
	if i := strings.LastIndex(target, "@"); i != -1 {
		return target[:i], target[i+1:]
	}
	return "", target
}

// hostBlockRe matches the start of our managed block for a given alias
// (Host lines are matched at the start of a line, whitespace-tolerant).
func hostBlockRe(alias string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*Host\s+` + regexp.QuoteMeta(alias) + `\s*$`)
}

// hasSSHAlias reports whether ~/.ssh/config already defines the given alias
// (ours or hand-written — either way `ssh <alias>` works).
func hasSSHAlias(cfg config, alias string) bool {
	b, err := os.ReadFile(filepath.Join(truckSSHDir(cfg), "config"))
	if err != nil {
		return false
	}
	return hostBlockRe(alias).Match(b)
}

// hasTruckAlias reports whether ~/.ssh/config already defines the local
// alias for this vehicle (ours or hand-written).
func hasTruckAlias(cfg config, vehicle string) bool {
	if !vehicleIDRe.MatchString(vehicle) {
		return false
	}
	return hasSSHAlias(cfg, truckAlias(vehicle))
}

// resolveTruckTarget picks what the fetch SSHes to: the per-truck alias when
// one is configured for the requested vehicle, otherwise TRUCK_SSH_TARGET.
func resolveTruckTarget(cfg config, vehicle string) string {
	if vehicle != "" && hasTruckAlias(cfg, vehicle) {
		return truckAlias(vehicle)
	}
	return cfg.TruckSSHTarget
}

// truckConfigBlock renders the managed ~/.ssh/config block for one alias.
// identity is the per-truck identity file (shared by the local and remote
// aliases); the known-hosts file is per alias so each connection's host
// key is stored separately.
func truckConfigBlock(cfg config, alias, hostName, identity string) string {
	user, _ := splitTarget(cfg.TruckSSHTarget)
	dir := truckSSHDir(cfg)
	var b strings.Builder
	fmt.Fprintf(&b, "\n# %s — added by Master Checklist truck SSH setup\n", alias)
	fmt.Fprintf(&b, "Host %s\n", alias)
	fmt.Fprintf(&b, "    HostName %s\n", hostName)
	if user != "" {
		fmt.Fprintf(&b, "    User %s\n", user)
	}
	fmt.Fprintf(&b, "    IdentityFile %s\n", filepath.Join(dir, identity))
	b.WriteString("    IdentitiesOnly yes\n")
	fmt.Fprintf(&b, "    UserKnownHostsFile %s\n", filepath.Join(dir, "known_hosts.d", alias))
	b.WriteString("    StrictHostKeyChecking accept-new\n")
	return b.String()
}

// truckSetupResult is the /api/truck/ssh_setup response.
type truckSetupResult struct {
	Vehicle       string `json:"vehicle"`
	Alias         string `json:"alias"`               // "truck-805"
	KeyPath       string `json:"key_path"`            // private key path
	PublicKey     string `json:"public_key"`          // one-line public key
	KeyCreated    bool   `json:"key_created"`         // false if it already existed
	ConfigAdded   bool   `json:"config_added"`        // false if the Host block already existed
	KeyInstalled  bool   `json:"key_installed"`       // public key landed in the truck's authorized_keys
	InstallDetail string `json:"install_detail"`      // how it was installed, or why it wasn't
	NextStep      string `json:"next_step,omitempty"` // manual command when the install failed

	// Remote login. RemoteSource says where the IP came from: "tailscale"
	// (looked up as truck-<N>-primarypc) or "manual" (typed into the card).
	// RemoteNote carries warnings (e.g. the truck is offline in Tailscale,
	// or the lookup failed).
	RemoteAlias       string `json:"remote_alias,omitempty"`
	RemoteHost        string `json:"remote_host,omitempty"`
	RemoteLogin       string `json:"remote_login,omitempty"`
	RemoteSource      string `json:"remote_source,omitempty"`
	RemoteConfigAdded bool   `json:"remote_config_added"` // no omitempty: false means "already existed"
	RemoteNote        string `json:"remote_note,omitempty"`
}

// appendConfigBlock writes one managed block to ~/.ssh/config, unless the
// alias already exists. Returns whether it wrote.
func appendConfigBlock(cfg config, block, alias string) (bool, error) {
	dir := truckSSHDir(cfg)
	if hasSSHAlias(cfg, alias) {
		return false, nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "config"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("could not open %s: %w", filepath.Join(dir, "config"), err)
	}
	_, werr := f.WriteString(block)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return false, fmt.Errorf("could not write %s: %v %v", filepath.Join(dir, "config"), werr, cerr)
	}
	return true, nil
}

// tailscalePeer is the subset of `tailscale status --json` that matters here.
type tailscalePeer struct {
	HostName     string   `json:"HostName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
}

// tailscaleStatus is the shape of `tailscale status --json` (Peer keyed by
// stable node key; a node can appear more than once in a large tailnet).
type tailscaleStatus struct {
	Peer map[string]tailscalePeer `json:"Peer"`
}

// lookupTailscaleTruck finds the truck's primary PC in the tailnet — the
// trucks register as "truck-<N>-primarypc" (with a truck-<N>-backuppc next
// to them, which is deliberately not picked) — and returns its Tailscale
// IPv4 and whether it is currently online. The tailnet can list the same
// node several times; duplicates with the same IP are collapsed, and if
// several distinct IPs remain the online one wins (an ambiguity among
// online nodes is an error listing them).
func lookupTailscaleTruck(ctx context.Context, cfg config, vehicle string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second) // the whole tailnet dump can be big on slow machines
	defer cancel()
	host := truckAlias(vehicle) + "-primarypc"
	cmd := exec.CommandContext(ctx, cfg.TruckTSBin, "status", "--json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", false, fmt.Errorf("`tailscale status` timed out")
		}
		return "", false, fmt.Errorf("could not ask Tailscale — is the laptop on the tailnet and the `tailscale` CLI installed? (%v)", truncate(strings.TrimSpace(stderr.String()), 120))
	}
	var status tailscaleStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return "", false, fmt.Errorf("could not parse `tailscale status` output: %v", err)
	}

	byIP := map[string]bool{} // ip -> any online sighting
	seen := map[string]bool{}
	var order []string
	for _, p := range status.Peer {
		if p.HostName != host {
			continue
		}
		for _, ip := range p.TailscaleIPs {
			// IPv4 only (100.x.y.z); skip the fd7a:… IPv6 twin.
			if !validRemoteIP(ip) {
				continue
			}
			// The same node can be listed several times — collapse by IP
			// (seen is tracked separately from byIP, which is only set for
			// online sightings, so an offline duplicate must not re-add
			// its IP to order).
			if !seen[ip] {
				seen[ip] = true
				order = append(order, ip)
			}
			if p.Online {
				byIP[ip] = true
			}
		}
	}
	switch len(order) {
	case 0:
		return "", false, fmt.Errorf("no machine named %q found in your Tailscale network (is the truck's primary PC on the tailnet?)", host)
	case 1:
		return order[0], byIP[order[0]], nil
	}
	// Several distinct IPs for the same name: prefer the online one.
	online := []string{}
	for _, ip := range order {
		if byIP[ip] {
			online = append(online, ip)
		}
	}
	switch len(online) {
	case 1:
		return online[0], true, nil
	case 0:
		return "", false, fmt.Errorf("%q exists with several IPs (%s) and none is online — enter the right IP manually", host, strings.Join(order, ", "))
	default:
		return "", false, fmt.Errorf("%q resolves to several online IPs (%s) — enter the right IP manually", host, strings.Join(online, ", "))
	}
}

// setupTruckSSH does the whole setup for one vehicle: identity, local
// config block, an optional remote-login block, key install. password is
// optional — when empty, the install uses whatever key/agent already works
// against the truck. manualRemoteIP is optional: when given (a validated
// IPv4) it is used for the `truck-<N>-remote` alias; when blank, the IP is
// looked up in Tailscale (truck-<N>-primarypc) and a lookup failure just
// skips the remote alias with a note. Only the key install can fail without
// stopping the rest: the identity and config are still in place, and the
// result says exactly what to run by hand.
func setupTruckSSH(ctx context.Context, cfg config, vehicle, password, manualRemoteIP string) (truckSetupResult, error) {
	if !vehicleAllowed(vehicle, cfg.VehicleRange) {
		return truckSetupResult{}, fmt.Errorf("the truck number is required and must be one of %s (got %q)", cfg.VehicleRange, vehicle)
	}
	if manualRemoteIP != "" && !validRemoteIP(manualRemoteIP) {
		return truckSetupResult{}, fmt.Errorf("the remote IP must be a plain IPv4 address like 100.65.197.86 (got %q)", manualRemoteIP)
	}
	alias := truckAlias(vehicle)
	dir := truckSSHDir(cfg)
	res := truckSetupResult{Vehicle: vehicle, Alias: alias, KeyPath: filepath.Join(dir, alias)}

	// The remote IP: typed in, or looked up via Tailscale. A lookup
	// failure never blocks the rest of the setup — it costs only the
	// remote alias, with a note saying why.
	remoteIP := manualRemoteIP
	if remoteIP != "" {
		res.RemoteSource = "manual"
	} else {
		ip, online, err := lookupTailscaleTruck(ctx, cfg, vehicle)
		switch {
		case err != nil:
			res.RemoteNote = "Remote login was skipped: " + err.Error() + ". Enter the IP manually to override."
		default:
			remoteIP = ip
			res.RemoteSource = "tailscale"
			if !online {
				res.RemoteNote = truckAlias(vehicle) + "-primarypc is currently offline in Tailscale — the alias was added anyway."
			}
		}
	}

	if err := os.MkdirAll(filepath.Join(dir, "known_hosts.d"), 0o700); err != nil {
		return res, fmt.Errorf("could not create %s: %w", dir, err)
	}

	// 1. Identity: one ed25519 key per truck, no passphrase (it is only ever
	//    used non-interactively by this app), commented with the alias.
	if _, err := os.Stat(res.KeyPath); os.IsNotExist(err) {
		keygen := exec.CommandContext(ctx, cfg.TruckKeygenBin, "-q", "-t", "ed25519", "-N", "", "-C", alias, "-f", res.KeyPath)
		var stderr bytes.Buffer
		keygen.Stderr = &stderr
		if err := keygen.Run(); err != nil {
			return res, fmt.Errorf("ssh-keygen failed: %v (%s)", err, truncate(strings.TrimSpace(stderr.String()), 200))
		}
		res.KeyCreated = true
	}
	pub, err := os.ReadFile(res.KeyPath + ".pub")
	if err != nil {
		return res, fmt.Errorf("could not read %s.pub: %w", res.KeyPath, err)
	}
	res.PublicKey = strings.TrimSpace(string(pub))

	// 2. Config blocks, appended once each. The local block points at the
	//    shared cable address; the remote block (when a remote IP is known)
	//    at the truck's own remote IP — same identity either way, since the
	//    authorized key on the truck is the same one.
	_, localHost := splitTarget(cfg.TruckSSHTarget)
	added, err := appendConfigBlock(cfg, truckConfigBlock(cfg, alias, localHost, alias), alias)
	if err != nil {
		return res, err
	}
	res.ConfigAdded = added

	if remoteIP != "" {
		remoteAlias := truckRemoteAlias(vehicle)
		user, _ := splitTarget(cfg.TruckSSHTarget)
		res.RemoteAlias = remoteAlias
		res.RemoteHost = remoteIP
		res.RemoteLogin = user + "@" + remoteIP
		added, err := appendConfigBlock(cfg, truckConfigBlock(cfg, remoteAlias, remoteIP, alias), remoteAlias)
		if err != nil {
			return res, err
		}
		res.RemoteConfigAdded = added
	}

	// 3. Install the public key on the truck.
	res.KeyInstalled, res.InstallDetail = installTruckKey(ctx, cfg, vehicle, res.PublicKey, password)
	if !res.KeyInstalled {
		user, host := splitTarget(cfg.TruckSSHTarget)
		target := host
		if user != "" {
			target = user + "@" + host
		}
		res.NextStep = fmt.Sprintf("ssh-copy-id -i %s.pub -o UserKnownHostsFile=%s %s",
			res.KeyPath, filepath.Join(dir, "known_hosts.d", alias), target)
	}
	return res, nil
}

// classifyInstallDetail diagnoses from ssh's stderr: an unreachable truck
// (the usual case when the laptop isn't cabled) must not be reported as
// "your key was not accepted".
func classifyInstallDetail(detail string) string {
	d := strings.ToLower(detail)
	switch {
	case strings.Contains(d, "timed out") || strings.Contains(d, "timeout"):
		return "could not reach the truck (connection timed out) — check the cable/network"
	case strings.Contains(d, "refused") || strings.Contains(d, "unreachable") || strings.Contains(d, "no route"):
		return "could not reach the truck (" + detail + ")"
	default:
		return "your existing key was not accepted (" + detail + ")"
	}
}

// authorizedKeysScript appends the key read from stdin to the truck's
// authorized_keys unless it is already there. Fixed script; the key comes in
// on stdin, never in the command line.
const authorizedKeysScript = `umask 077; mkdir -p ~/.ssh; key=$(cat); ` +
	`grep -qxF -- "$key" ~/.ssh/authorized_keys 2>/dev/null || printf '%s\n' "$key" >> ~/.ssh/authorized_keys`

// installTruckKey pushes the public key to the truck. It connects to the raw
// address (the alias's own key isn't authorized yet) but already records
// the host key under the alias's known-hosts file, so the very first
// contact is stored where later alias connections will look for it.
//
// Two attempts: first with whatever identity/agent already works (BatchMode,
// so it fails fast instead of prompting); then, if a password was given,
// with it via SSH_ASKPASS in a detached session so ssh never opens a tty.
func installTruckKey(ctx context.Context, cfg config, vehicle, publicKey, password string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, truckSetupTimeout)
	defer cancel()

	alias := truckAlias(vehicle)
	knownHosts := filepath.Join(truckSSHDir(cfg), "known_hosts.d", alias)
	common := []string{
		"-o", "ConnectTimeout=5",
		"-o", "IdentitiesOnly=no", // let the existing keys/agent try
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "StrictHostKeyChecking=accept-new",
	}

	run := func(extra []string, env []string, detach bool) (string, error) {
		args := append(append([]string{}, common...), extra...)
		args = append(args, cfg.TruckSSHTarget, authorizedKeysScript)
		cmd := exec.CommandContext(ctx, cfg.TruckSSHBin, args...)
		cmd.Stdin = strings.NewReader(publicKey + "\n")
		cmd.Env = append(os.Environ(), env...)
		if detach {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		return truncate(strings.TrimSpace(stderr.String()), 200), err
	}

	// Attempt 1: existing credentials.
	if detail, err := run([]string{"-o", "BatchMode=yes"}, nil, false); err == nil {
		return true, "installed using your existing SSH key/agent"
	} else if password == "" {
		if ctx.Err() == context.DeadlineExceeded {
			return false, "timed out reaching the truck"
		}
		diagnosed := classifyInstallDetail(detail)
		if strings.Contains(diagnosed, "key was not accepted") {
			diagnosed += " — give the truck's login password to install the key"
		}
		return false, diagnosed
	}

	// Attempt 2: the one-time password, handed to ssh through SSH_ASKPASS.
	// The helper reads it from the environment — it never appears on a
	// command line or in a log.
	askpass, err := writeAskpassHelper()
	if err != nil {
		return false, "could not prepare the password helper: " + err.Error()
	}
	defer os.Remove(askpass)
	env := []string{
		"SSH_ASKPASS=" + askpass,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=:0", // older OpenSSH only consults SSH_ASKPASS when DISPLAY is set
		"TRUCK_SSH_PASSWORD=" + password,
	}
	detail, err := run([]string{"-o", "NumberOfPasswordPrompts=1", "-o", "PreferredAuthentications=password,keyboard-interactive"}, env, true)
	if err == nil {
		return true, "installed using the password you entered"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return false, "timed out reaching the truck"
	}
	diagnosed := strings.Replace(classifyInstallDetail(detail),
		"your existing key was not accepted", "the password was not accepted", 1)
	return false, diagnosed
}

// writeAskpassHelper writes a tiny executable that prints the password from
// TRUCK_SSH_PASSWORD; ssh runs it in place of a terminal prompt.
func writeAskpassHelper() (string, error) {
	f, err := os.CreateTemp("", "truck-askpass-*.sh")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString("#!/bin/sh\nprintf '%s' \"$TRUCK_SSH_PASSWORD\"\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("%v %v", werr, cerr)
	}
	if err := os.Chmod(f.Name(), 0o700); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// makeTruckIPLookupHandler serves GET /api/truck/ssh_lookup?vehicle=N — just
// the Tailscale lookup, so the UI can autofill the remote-IP field as soon
// as the truck number is typed. Same local-only gate as the setup: the
// hosted app has no tailscale to ask. A truck missing from the tailnet is
// a normal outcome for the form (the user may type the IP by hand), so it
// comes back 200 with the reason in "error" and an empty ip.
func makeTruckIPLookupHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !cfg.TruckSSHEnabled && !cfg.DryRun {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "The remote-IP lookup only works when the app runs locally — it asks the tailscale CLI on this laptop.",
			})
			return
		}
		vehicle := strings.TrimSpace(r.FormValue("vehicle"))
		if !vehicleAllowed(vehicle, cfg.VehicleRange) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("Enter the truck number first (must be one of %s).", cfg.VehicleRange),
			})
			return
		}
		ip, online, err := lookupTailscaleTruck(r.Context(), cfg, vehicle)
		if err != nil {
			log.Printf("truck ip lookup (vehicle %q): %v", vehicle, err)
			writeJSON(w, http.StatusOK, map[string]any{"ip": "", "online": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ip":     ip,
			"online": online,
			"host":   truckAlias(vehicle) + "-primarypc",
		})
	}
}

// makeTruckSSHSetupHandler serves POST /api/truck/ssh_setup with form fields
// vehicle (required), password (optional) and remote_ip (optional — the
// truck's remote/VPN address, which adds a `truck-<N>-remote` alias).
// Same local-only gate as the fetch; a dry run performs the real local
// steps (key + config) but reports the install as skipped, so the flow can
// be tried without a truck.
func makeTruckSSHSetupHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !cfg.TruckSSHEnabled && !cfg.DryRun {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "SSH setup only works when the app runs locally (TRUCK_SSH_ENABLED=true) on the laptop that will talk to the truck — the hosted app has no ~/.ssh of yours to write to.",
			})
			return
		}
		vehicle := strings.TrimSpace(r.FormValue("vehicle"))
		password := r.FormValue("password")
		remoteIP := strings.TrimSpace(r.FormValue("remote_ip"))
		if !vehicleAllowed(vehicle, cfg.VehicleRange) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("Enter the truck number in the Vehicle field first — the SSH identity and alias are named after it (must be one of %s).", cfg.VehicleRange),
			})
			return
		}
		if remoteIP != "" && !validRemoteIP(remoteIP) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("The remote IP must be a plain IPv4 address like 100.65.197.86 (got %q).", remoteIP),
			})
			return
		}

		if cfg.DryRun && !cfg.TruckSSHEnabled {
			// Local steps for real, remote step skipped.
			dry := cfg
			dry.TruckSSHBin = "false" // any ssh attempt fails immediately
			res, err := setupTruckSSH(r.Context(), dry, vehicle, "", remoteIP)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			res.InstallDetail = "dry run — the public key was not sent to a truck"
			writeJSON(w, http.StatusOK, res)
			return
		}

		res, err := setupTruckSSH(r.Context(), cfg, vehicle, password, remoteIP)
		if err != nil {
			log.Printf("truck ssh setup (vehicle %q): %v", vehicle, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("truck ssh setup (vehicle %q remote %q): key_created=%v config_added=%v remote_config_added=%v key_installed=%v",
			vehicle, remoteIP, res.KeyCreated, res.ConfigAdded, res.RemoteConfigAdded, res.KeyInstalled)
		writeJSON(w, http.StatusOK, res)
	}
}
