# Master Checklist — AD Smoke Test → Confluence

*[日本語版はこちら](README.ja.md)*

A small web app (hosted on `apps.applied.dev`) for logging autonomous-driving smoke test
runs. A test engineer fills out a checklist form and, on submit, the app creates a
Confluence page recording the run under the [Master Testing](https://appliedintuition.atlassian.net/wiki/spaces/NEURON/pages/2693234852/Master+Testing)
page in the `NEURON` space, filed under a page for that month (created automatically the
first time it's needed).

Defaults to dark mode; toggle to light mode or switch between English/Japanese from the
header.

## Checklist fields

- Test Type (Master Testing / Candidate Testing), Tag (autocompletes from GitHub tags on
  [Ext-Applied-Frontier/brain2](https://github.com/Ext-Applied-Frontier/brain2/tags) —
  `scheduled-night` tags for Master, `candidate` tags for Candidate), Date, Vehicle
  (autofill 801–835, see `VEHICLE_RANGE`), Test Engineer (pre-filled with the signed-in
  user, autofill from the access groups), Commit Hash, Slack Thread, Recording (Google
  Drive link), Run ID, Overall Result
- Preflight: `run_syscheck` results, `check_timesync` results, software build & launch,
  health monitor healthy, logs recording in `/media/hotswap1/frontier/`
- Engagement checks
- Disengagement checks: Run ID, Closed Loop Run ID, steering left, steering right,
  accel, brake, cruise control, e-stop, AD/MD button

## "What changed since the previous build"

The first section of the form summarises what changed in `brain2` between the selected Tag
and the previous tag of the same kind (e.g. `trucking-scheduled-night-2026-08-31` →
`…-2026-09-01`; for Candidate runs the previous `trucking-candidate-*` tag). Only the
tester's selection is diffed — never the whole history. The base tag can be overridden in
**Compare against**. `GET /api/diff?head=<tag>[&base=<tag>]` does the work (`diff.go`) and the
browser posts the rendered JSON back with the form so the same summary lands at the top of
the Confluence page, along with optional tester notes.

It is written for non-technical readers:

- Headline sentence: `Aug 31 → Sep 1: 92 changes in total — HMI 0 · Behavior 1 · Planner 4 …`
- Each area shows a one-line explanation ("what the driver sees and hears") and lists only
  **described** changes as a clean headline (Jira key, `(#PR)` and `[Team]` tags stripped) plus
  the first line of the PR description; ticket/PR/tags/paths sit behind a **Details** toggle.
- Automated or housekeeping commits (`Vehicle OS Change` Copybara syncs, `[auto]` bumps,
  lockfile updates) are never listed — they are **counted**: "N more automated or undescribed
  changes also touched this area".
- Everything outside the five areas is a single count line: "N other changes outside these
  areas (M automated system updates) — Full list on GitHub".

Classification is by the paths each commit touched (one GitHub call per commit, up to 200,
cached 30 min) plus keywords in the title:

| Category | Paths | Title keywords |
|---|---|---|
| HMI | `onroad/hmi/`, `trucking/hmi/`, `vehicle_os/hmi/` | `hmi` |
| Behavior | `onroad/behavior/` (except planning/prediction), `common/behavior/`, `onroad/ml/behavior/`, `trucking/fallback/behavior*` | `behavior` |
| Planner | `onroad/behavior/planning/`, `onroad/cmas/planning/`, `trucking/planning/`, `trucking/fallback/planning*`, `trucking/interfaces/*planner_nodes/` | `planner`, `planning` |
| Prediction | `onroad/behavior/prediction/`, `onroad/ml_optimization/prediction/` | `prediction`, `predictor` |
| Bug fixes & reverts | any of the above or none | `fix`, `bug`, `hotfix`, `regression`, `crash`, `resolve`, `revert` |

A commit can appear under several categories. When the app's service account has
`roles/aiplatform.user`, Gemini also writes an "In short" bullet summary in plain language
(`ai_summary`); otherwise that part is omitted with a note.

## Test Engineer autofill

The Test Engineer field is pre-filled with the signed-in user's name (derived from the
IAP `X-Goog-Authenticated-User-Email` header) and offers a dropdown of the members of the
access groups (`ENGINEER_GROUPS`, default `okta-team-vehicle-testing`, `okta-ext-frontier`,
`okta-ext-vehicle_operators-jp`). Names are derived from emails
(`brandon.moyer@ext.applied.co` → `Brandon Moyer`).

Membership is resolved in two layers (`engineers.go`):

1. **Live** — the Cloud Identity API using the app's service account. This currently
   fails (the SA has no visibility into these groups); it will start working, with no
   code change, if the apps-platform team grants `master-checklist-sa` read access to
   the groups.
2. **Fallback** — `engineers_snapshot.json`, embedded at build time. Refresh it with
   `scripts/refresh_engineers.sh` (uses your own gcloud login), commit, and redeploy.
   Groups your account cannot read keep their existing entries — as of Sep 2026 only
   `okta-team-vehicle-testing` is readable by ordinary members, so `okta-ext-frontier`
   and `okta-ext-vehicle_operators-jp` are empty until a group owner runs the script or
   their emails are added to the JSON by hand.

## Help / Bugs / Feedback

The **Help / Feedback** button in the header opens `/feedback`: a form (EN/JA, with its own
language selector) whose submission is emailed to `FEEDBACK_TO` (default
`john.pham@applied.co`).

- **Email transport** — the apps-platform [Data API](https://apps.applied.dev/docs/data-api)
  Gmail integration, sending *as the signed-in user* (`gmail.send` scope only). The first
  time someone submits, the app answers `needs_connect` and the page shows a **Connect
  Gmail** button that opens the platform's OAuth popup; the message is re-sent once the
  popup closes. Connections are per environment (staging and prod are separate).
  The Data API needs the platform-injected `X-Request-Token`, which is only added when the
  browser carries the `trident=true` cookie — the app sets it on every page load.
- **Translation** — Japanese text (hiragana/katakana/kanji) is translated to English with
  Gemini on Vertex AI (`TRANSLATE_MODEL`, default `gemini-2.5-flash`, in `VERTEX_LOCATION`)
  before sending; the email contains the English translation followed by the original.
  This requires `roles/aiplatform.user` for `master-checklist-sa` in each project — until
  it is granted, translation fails with 403 and the email carries the original text plus a
  note (`Automatic translation was unavailable: …`).
- **Email layout** — subject `[Master Checklist] <Bug report|Help request|Feedback>: <English
  headline>`; body with submitter, time, environment/revision, UI language, page, the
  translation (if any), the original message and the user agent.
- **Local testing** — run `apps-platform app forwarder --service master-checklist`, export
  the `SOCKS_PORT`, `DATA_API_URL`, `DATA_API_AUTH_TOKEN`, `X_REQUEST_TOKEN` lines it prints,
  and start the app; `/api/feedback` then talks to the real Data API as you.

## Local development

```sh
go run .
```

By default this tries to read the `confluence-token` secret from Secret Manager, which
won't work locally. Run with a dry run instead — it logs the Confluence payload it would
have sent instead of calling the API:

```sh
CONFLUENCE_DRY_RUN=true go run .
```

Then open http://localhost:8080.

To exercise the real Confluence/GitHub integrations locally, bypass Secret Manager by
passing the tokens as env vars (never commit these):

```sh
CONFLUENCE_TOKEN="<atlassian-api-token>" GITHUB_TOKEN="<github-pat>" go run .
```

## Config (env vars)

| Var | Default | Purpose |
|---|---|---|
| `CONFLUENCE_BASE_URL` | `https://appliedintuition.atlassian.net/wiki` | Confluence base URL |
| `CONFLUENCE_SPACE_KEY` | `NEURON` | Space to create pages in |
| `CONFLUENCE_PARENT_PAGE_ID` | `2693234852` | Master Testing page ID |
| `CONFLUENCE_BOT_EMAIL` | — | Bot account email used for Basic Auth |
| `CONFLUENCE_CANDIDATE_PARENT_PAGE_ID` | `2909896800` | Candidate Testing folder ID |
| `GITHUB_TAG_REPO_OWNER` | `Ext-Applied-Frontier` | Owner of the repo to autocomplete tags from |
| `GITHUB_TAG_REPO_NAME` | `brain2` | Repo to autocomplete tags from |
| `ADDR` | `:8080` | HTTP listen address |
| `CONFLUENCE_DRY_RUN` | `false` | Skip Secret Manager + Confluence/GitHub calls, log instead |
| `ENGINEER_GROUPS` | vehicle-testing, ext-frontier, ext-vehicle_operators-jp | Groups whose members populate the Test Engineer dropdown |
| `VEHICLE_RANGE` | `801-835` | Vehicle IDs offered in the Vehicle dropdown; ranges and/or single IDs, comma-separated (e.g. `801-840,900`) |
| `FEEDBACK_TO` | `john.pham@applied.co` | Recipient of Help/Bug/Feedback emails |
| `TRANSLATE_MODEL` | `gemini-2.5-flash` | Vertex AI model used to translate Japanese feedback to English |
| `VERTEX_LOCATION` | `us-central1` | Vertex AI region |
| `TRANSLATE_DISABLED` | `false` | Skip translation entirely |
| `PROJECT_ID`, `URL_BASE` | injected by apps-platform | Used for Vertex AI and the Data API base URL (`https://dataapi.$URL_BASE`) |
| `CONFLUENCE_TOKEN` | — | Local dev only: use this Atlassian API token instead of Secret Manager |
| `GITHUB_TOKEN` | — | Local dev only: use this GitHub PAT instead of Secret Manager |

## Secrets & deploy (apps-platform)

`project.toml` has `enable_secrets = true`. Upload the two tokens once per environment:

- `confluence-token` — an **Atlassian API token** created by the `CONFLUENCE_BOT_EMAIL`
  account at https://id.atlassian.com/manage-profile/security/api-tokens (starts with
  `ATATT3…`). Confluence reports a bad/foreign token as an *anonymous* caller (v1 403
  "cannot access Confluence", v2 404 NOT_FOUND) rather than a 401, so a wrong token
  looks like an outage in the UI.
- `github-token` — a GitHub PAT that can read `Ext-Applied-Frontier/brain2`. For a
  fine-grained PAT the **resource owner must be the `Ext-Applied-Frontier` org** with
  `brain2` selected and `Contents: Read`; a personal-owner PAT sees the org but gets
  404 on the repo. A classic PAT needs the `repo` scope and SSO authorization for the org.

```sh
# staging (default profile)
apps-platform app secret set confluence-token "<atlassian-api-token>"
apps-platform app secret set github-token "<github-pat>"
apps-platform app deploy

# prod
apps-platform app --environment experimental-prod secret set confluence-token "<atlassian-api-token>"
apps-platform app --environment experimental-prod secret set github-token "<github-pat>"
apps-platform app --environment experimental-prod deploy
```

The app reads these back at
`projects/$PROJECT_ID/secrets/master-checklist-<name>/versions/latest` via the Secret
Manager client and caches each value for 5 minutes (`secrets.go`), so a rotated secret
takes effect without a redeploy.

Deploys go to the profile in `~/.apps-platform/environment.yaml` (`experimental-staging`
→ `master-checklist.experimental.staging.apps.applied.dev`; `experimental-prod` →
`master-checklist.experimental.apps.applied.dev`). Note: `gcloud` ≥ 583 breaks
`apps-platform app deploy` with `PERMISSION_DENIED run.locations.uploadSource`; pin
`gcloud components update --version 570.0.0` until the platform team fixes it.

## Access control

This app should be restricted to Applied FTEs and the `ext-frontier` external group.
That restriction is configured at the `apps-platform` project level (not in this app's
code) — set it up in the apps-platform project settings after the first deploy.
