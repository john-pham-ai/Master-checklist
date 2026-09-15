package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Fetching the latest run_id from a test truck over SSH.
//
// This is a LOCAL-ONLY feature: the deployed (Cloud Run) app has no route to
// the trucks, so it is disabled there and the form simply doesn't render the
// fetch buttons. Locally, the app shells out to the system `ssh`, which means
// the developer's own SSH setup is used as-is — ~/.ssh/config, keys, agent
// and known_hosts — no secrets need to be managed by the app.
//
// Security: the browser only ever sends a vehicle ID (validated digits-only
// and against VEHICLE_RANGE, or omitted entirely); the remote command is a
// fixed server-built script. Nothing from the request reaches the shell.

// truckSSHTimeout bounds the whole SSH round trip, so a hung connection can't
// pin a browser fetch forever.
const truckSSHTimeout = 10 * time.Second

// truckHostRe extracts the vehicle number from the truck's hostname, e.g.
// "truck-805-primarypc" -> "805".
var truckHostRe = regexp.MustCompile(`^truck-([0-9]+)-`)

// vehicleIDRe accepts only digits — the single thing the browser may send
// that ends up near a shell command.
var vehicleIDRe = regexp.MustCompile(`^[0-9]+$`)

// vehicleAllowed reports whether id is a plain number present in the
// configured VEHICLE_RANGE.
func vehicleAllowed(id, rangeSpec string) bool {
	if !vehicleIDRe.MatchString(id) {
		return false
	}
	for _, v := range parseVehicleRange(rangeSpec) {
		if v == id {
			return true
		}
	}
	return false
}

// truckRemoteScript is the fixed script executed on the truck. {ROOT} is
// replaced with the configured log root. The script derives the vehicle
// number from the truck's own hostname (the laptop is cabled to one truck at
// a time), prints a marker if that fails, then reports the newest run
// directory for today (truck clock) and the newest overall.
//
// Log layout (per the trucks' own convention):
//
//	$ROOT/truck-805/2026/09/15/2026-09-15_14-48-57_truck-805
//	                   └ year/month/day ┘ └───── run_id ─────┘
//
// `ls | sort` is chronological because the zero-padded names sort lexically.
func truckRemoteScript(logRoot string) string {
	const script = `set -u
host=$(hostname)
printf 'HOSTNAME\t%s\n' "$host"
n=""
case "$host" in
  truck-[0-9]*-*) n=${host#truck-}; n=${n%%-*} ;;
  *) printf 'NOVEHICLE\n'; exit 0 ;;
esac
root='{ROOT}/truck-'"$n"
today=$(date +%Y/%m/%d)
latest_today=$(ls -1d "$root/$today"/*_truck-"$n" 2>/dev/null | sort | tail -1)
latest=$(ls -1d "$root"/*/*/*/*_truck-"$n" 2>/dev/null | sort | tail -1)
printf 'TODAY\t%s\nLATEST\t%s\n' "$latest_today" "$latest"
`
	return strings.ReplaceAll(script, "{ROOT}", logRoot)
}

// truckRunInfo is the /api/truck/run_id response.
type truckRunInfo struct {
	Vehicle string `json:"vehicle"` // vehicle number derived from the hostname
	RunID   string `json:"run_id"`  // run directory name, e.g. 2026-09-15_14-48-57_truck-805
	Path    string `json:"path"`    // absolute path of that run directory on the truck
	Date    string `json:"date"`    // the run's day on the truck, e.g. 2026/09/15
	Host    string `json:"hostname"`
	Warning string `json:"warning,omitempty"`
}

// truckDateRe pulls the year/month/day out of a run path.
var truckDateRe = regexp.MustCompile(`(\d{4})/(\d{2})/(\d{2})/`)

// lastPathSegment returns the part after the final slash.
func lastPathSegment(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i != -1 {
		return p[i+1:]
	}
	return p
}

// parseTruckOutput interprets the script's tab-separated output lines and
// picks the run to report: the newest from today when there is one, otherwise
// the newest overall plus a warning. requested is the vehicle number the
// browser asked about, if any; a mismatch with the connected truck is a
// warning, not an error.
func parseTruckOutput(out, requested string) (truckRunInfo, error) {
	var host, today, latest string
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch key {
		case "HOSTNAME":
			host = val
		case "TODAY":
			today = val
		case "LATEST":
			latest = val
		}
	}
	if strings.Contains(out, "NOVEHICLE") {
		return truckRunInfo{}, fmt.Errorf("the connected host %q doesn't look like a truck (expected truck-<number>-…); check TRUCK_SSH_TARGET", host)
	}
	if host == "" {
		return truckRunInfo{}, fmt.Errorf("unexpected SSH output (no HOSTNAME line): %q", truncate(out, 200))
	}
	m := truckHostRe.FindStringSubmatch(host)
	if m == nil {
		return truckRunInfo{}, fmt.Errorf("the connected host %q doesn't look like a truck (expected truck-<number>-…); check TRUCK_SSH_TARGET", host)
	}
	info := truckRunInfo{Vehicle: m[1], Host: host}

	switch {
	case today != "":
		info.Path = today
	case latest != "":
		info.Path = latest
		info.Warning = "No runs found for today on the truck's clock — this is the latest run from an earlier day."
	default:
		return info, fmt.Errorf("no run log directories found on the truck under the configured TRUCK_LOG_ROOT")
	}
	info.RunID = lastPathSegment(info.Path)
	if d := truckDateRe.FindStringSubmatch(info.Path); d != nil {
		info.Date = d[1] + "/" + d[2] + "/" + d[3]
	}
	if requested != "" && requested != info.Vehicle {
		if info.Warning != "" {
			info.Warning += " "
		}
		info.Warning += fmt.Sprintf("The connected truck is %s, not %s — check the Vehicle field.", info.Vehicle, requested)
	}
	return info, nil
}

// fetchTruckRunID SSHes to the configured target, runs the fixed script and
// parses its output.
func fetchTruckRunID(ctx context.Context, cfg config, requested string) (truckRunInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, truckSSHTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cfg.TruckSSHBin,
		"-o", "BatchMode=yes", // never prompt for a password — fail fast instead
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=accept-new", // first cable-up to a new truck shouldn't hang on a yes/no prompt
		cfg.TruckSSHTarget, "bash -s")
	cmd.Stdin = strings.NewReader(truckRemoteScript(cfg.TruckLogRoot))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	// An ssh-level failure (exit 255) can leave the remote script's output
	// partially captured before the connection drops, so classify the error
	// from the exit status/stderr — never try to parse truncated stdout.
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return truckRunInfo{}, fmt.Errorf("SSH to %s timed out after %s", cfg.TruckSSHTarget, truckSSHTimeout)
		}
		// ssh exits 255 for its own failures (unreachable, key rejected), as
		// opposed to a nonzero command on the far side.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 255 {
			return truckRunInfo{}, fmt.Errorf("could not SSH to %s — host unreachable or the key was rejected. Test it yourself with `ssh %s` (%s)",
				cfg.TruckSSHTarget, cfg.TruckSSHTarget, truncate(strings.TrimSpace(stderr.String()), 200))
		}
		return truckRunInfo{}, fmt.Errorf("ssh failed: %v (%s)", err, truncate(strings.TrimSpace(stderr.String()), 200))
	}
	return parseTruckOutput(string(out), requested)
}

// makeTruckRunIDHandler serves GET /api/truck/run_id?vehicle=<number>. The
// vehicle parameter is optional: the connected truck is identified by its
// hostname, and the response reports which one it was.
//
// TRUCK_SSH_ENABLED=true always means the REAL SSH fetch, even alongside
// CONFLUENCE_DRY_RUN (so the Confluence side can stay in dry run while the
// truck fetch is live). Without it, a dry run serves a deterministic fake so
// the UI flow can be exercised without a truck.
func makeTruckRunIDHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.TruckSSHEnabled && !cfg.DryRun {
			http.Error(w, "truck SSH is not enabled", http.StatusNotFound)
			return
		}
		requested := strings.TrimSpace(r.URL.Query().Get("vehicle"))
		if requested != "" && !vehicleAllowed(requested, cfg.VehicleRange) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid vehicle number %q", requested)})
			return
		}

		if !cfg.TruckSSHEnabled {
			// Dry run without SSH enabled: deterministic fake run so the UI
			// flow can be tested without a truck.
			vehicle := requested
			if vehicle == "" {
				vehicle = "805"
			}
			now := time.Now()
			dir := now.Format("2006-01-02_15-04-05") + "_truck-" + vehicle
			date := now.Format("2006/01/02")
			writeJSON(w, http.StatusOK, truckRunInfo{
				Vehicle: vehicle, RunID: dir,
				Path: cfg.TruckLogRoot + "/truck-" + vehicle + "/" + date + "/" + dir,
				Date: date, Host: "truck-" + vehicle + "-primarypc",
			})
			return
		}

		info, err := fetchTruckRunID(r.Context(), cfg, requested)
		if err != nil {
			log.Printf("truck run_id fetch (vehicle %q): %v", requested, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, info)
	}
}
