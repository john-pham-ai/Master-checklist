package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// setupTruckSSH creates the identity, appends that block to ~/.ssh/config
// (idempotently) and installs the public key on the truck. The name is
// forced: the only input is the vehicle number, validated like the fetch.
// After setup, fetches for that vehicle SSH to the alias instead of the raw
// address (resolveTruckTarget).

// truckSetupTimeout bounds the key-install SSH round trip.
const truckSetupTimeout = 20 * time.Second

// truckAlias is the SSH Host alias for a vehicle number: "truck-805".
func truckAlias(vehicle string) string { return "truck-" + vehicle }

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

// hasTruckAlias reports whether ~/.ssh/config already defines the alias for
// this vehicle (ours or hand-written — either way `ssh truck-805` works).
func hasTruckAlias(cfg config, vehicle string) bool {
	if !vehicleIDRe.MatchString(vehicle) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(truckSSHDir(cfg), "config"))
	if err != nil {
		return false
	}
	return hostBlockRe(truckAlias(vehicle)).Match(b)
}

// resolveTruckTarget picks what the fetch SSHes to: the per-truck alias when
// one is configured for the requested vehicle, otherwise TRUCK_SSH_TARGET.
func resolveTruckTarget(cfg config, vehicle string) string {
	if vehicle != "" && hasTruckAlias(cfg, vehicle) {
		return truckAlias(vehicle)
	}
	return cfg.TruckSSHTarget
}

// truckConfigBlock renders the managed ~/.ssh/config block for a vehicle.
func truckConfigBlock(cfg config, vehicle string) string {
	alias := truckAlias(vehicle)
	user, host := splitTarget(cfg.TruckSSHTarget)
	dir := truckSSHDir(cfg)
	var b strings.Builder
	fmt.Fprintf(&b, "\n# %s — added by Master Checklist truck SSH setup\n", alias)
	fmt.Fprintf(&b, "Host %s\n", alias)
	fmt.Fprintf(&b, "    HostName %s\n", host)
	if user != "" {
		fmt.Fprintf(&b, "    User %s\n", user)
	}
	fmt.Fprintf(&b, "    IdentityFile %s\n", filepath.Join(dir, alias))
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
}

// setupTruckSSH does the whole setup for one vehicle: identity, config
// block, key install. password is optional — when empty, the install uses
// whatever key/agent already works against the truck. Only the key install
// can fail without stopping the rest: the identity and config are still in
// place, and the result says exactly what to run by hand.
func setupTruckSSH(ctx context.Context, cfg config, vehicle, password string) (truckSetupResult, error) {
	if !vehicleAllowed(vehicle, cfg.VehicleRange) {
		return truckSetupResult{}, fmt.Errorf("the truck number is required and must be one of %s (got %q)", cfg.VehicleRange, vehicle)
	}
	alias := truckAlias(vehicle)
	dir := truckSSHDir(cfg)
	res := truckSetupResult{Vehicle: vehicle, Alias: alias, KeyPath: filepath.Join(dir, alias)}

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

	// 2. Config block, appended once.
	if !hasTruckAlias(cfg, vehicle) {
		f, err := os.OpenFile(filepath.Join(dir, "config"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return res, fmt.Errorf("could not open %s: %w", filepath.Join(dir, "config"), err)
		}
		_, werr := f.WriteString(truckConfigBlock(cfg, vehicle))
		cerr := f.Close()
		if werr != nil || cerr != nil {
			return res, fmt.Errorf("could not write %s: %v %v", filepath.Join(dir, "config"), werr, cerr)
		}
		res.ConfigAdded = true
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
		return false, "your existing key was not accepted and no password was given (" + detail + ")"
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
	return false, "the password was not accepted (" + detail + ")"
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

// makeTruckSSHSetupHandler serves POST /api/truck/ssh_setup with form fields
// vehicle (required) and password (optional). Same local-only gate as the
// fetch; a dry run performs the real local steps (key + config) but reports
// the install as skipped, so the flow can be tried without a truck.
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
		if !vehicleAllowed(vehicle, cfg.VehicleRange) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("Enter the truck number in the Vehicle field first — the SSH identity and alias are named after it (must be one of %s).", cfg.VehicleRange),
			})
			return
		}

		if cfg.DryRun && !cfg.TruckSSHEnabled {
			// Local steps for real, remote step skipped.
			dry := cfg
			dry.TruckSSHBin = "false" // any ssh attempt fails immediately
			res, err := setupTruckSSH(r.Context(), dry, vehicle, "")
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			res.InstallDetail = "dry run — the public key was not sent to a truck"
			writeJSON(w, http.StatusOK, res)
			return
		}

		res, err := setupTruckSSH(r.Context(), cfg, vehicle, password)
		if err != nil {
			log.Printf("truck ssh setup (vehicle %q): %v", vehicle, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("truck ssh setup (vehicle %q): key_created=%v config_added=%v key_installed=%v", vehicle, res.KeyCreated, res.ConfigAdded, res.KeyInstalled)
		writeJSON(w, http.StatusOK, res)
	}
}
