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
  `scheduled-night` tags for Master, `candidate` tags for Candidate), Date, Vehicle,
  Test Engineer, Commit Hash, Slack Thread, Recording (Google Drive link), Run ID,
  Overall Result
- Preflight: `run_syscheck` results, `check_timesync` results, software build & launch,
  health monitor healthy, logs recording in `/media/hotswap1/frontier/`
- Engagement checks
- Disengagement checks: Run ID, Closed Loop Run ID, steering left, steering right,
  accel, brake, cruise control, e-stop, AD/MD button

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
