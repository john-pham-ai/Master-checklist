#!/usr/bin/env bash
# Regenerate engineers_snapshot.json (Test Engineer autofill fallback) from the
# Cloud Identity API using YOUR gcloud login. Groups that your account cannot
# read keep whatever entries the existing snapshot already has.
#
#   scripts/refresh_engineers.sh                 # default groups
#   ENGINEER_GROUPS=a@applied.co,b@applied.co scripts/refresh_engineers.sh
#
# Commit the result and redeploy for it to take effect.
set -euo pipefail
cd "$(dirname "$0")/.."

ENG_GROUPS="${ENGINEER_GROUPS:-okta-team-vehicle-testing@applied.co,okta-ext-frontier@applied.co,okta-ext-vehicle_operators-jp@applied.co}"

python3 - "$ENG_GROUPS" <<'PY'
import datetime, json, os, subprocess, sys

groups = [g.strip() for g in sys.argv[1].split(",") if g.strip()]
path = "engineers_snapshot.json"
old = {}
if os.path.exists(path):
    try:
        old = json.load(open(path)).get("groups", {})
    except Exception:
        pass

out = {}
for g in groups:
    r = subprocess.run(
        ["gcloud", "identity", "groups", "memberships", "list", f"--group-email={g}", "--format=json"],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        first = (r.stderr.strip().splitlines() or ["error"])[0]
        print(f"WARN {g}: not readable by you ({first[:120]}); keeping {len(old.get(g, []))} existing entries", file=sys.stderr)
        out[g] = old.get(g, [])
        continue
    members = json.loads(r.stdout or "[]")
    emails = sorted({m["preferredMemberKey"]["id"].lower() for m in members if "@" in m.get("preferredMemberKey", {}).get("id", "")})
    out[g] = emails
    print(f"{g}: {len(emails)} members", file=sys.stderr)

snap = {
    "generated": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "groups": out,
}
with open(path, "w") as f:
    json.dump(snap, f, indent=2)
    f.write("\n")
print(f"wrote {path}", file=sys.stderr)
PY
