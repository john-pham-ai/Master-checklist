# Master Checklist — AD Smoke Test → Confluence

A small web app (hosted on `apps.applied.dev`) for logging autonomous-driving smoke test
runs. A test engineer fills out a checklist form and, on submit, the app creates a
Confluence page recording the run under the [Master Testing](https://appliedintuition.atlassian.net/wiki/spaces/NEURON/pages/2693234852/Master+Testing)
page in the `NEURON` space, filed under a page for that month (created automatically the
first time it's needed).

Defaults to dark mode; toggle to light mode or switch between English/Japanese from the
header.

## Checklist fields

- Tag, Date, Vehicle, Test Engineer, Commit Hash, Slack Thread, Recording (Google Drive link), Run ID, Overall Result
- Preflight: `run_syscheck` results, `check_timesync` results, software build & launch,
  health monitor healthy, logs recording in `/media/hotswap1/frontier/`
- Engagement checks
- Disengagement checks: steering left, steering right, accel, brake, cruise control,
  e-stop, AD/MD button

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

## Config (env vars)

| Var | Default | Purpose |
|---|---|---|
| `CONFLUENCE_BASE_URL` | `https://appliedintuition.atlassian.net/wiki` | Confluence base URL |
| `CONFLUENCE_SPACE_KEY` | `NEURON` | Space to create pages in |
| `CONFLUENCE_PARENT_PAGE_ID` | `2693234852` | Master Testing page ID |
| `CONFLUENCE_BOT_EMAIL` | — | Bot account email used for Basic Auth |
| `ADDR` | `:8080` | HTTP listen address |
| `CONFLUENCE_DRY_RUN` | `false` | Skip Secret Manager + Confluence calls, log instead |

## Secrets & deploy (apps-platform)

`project.toml` has `enable_secrets = true`. Upload the Confluence API token once:

```sh
apps-platform app secret set confluence-token "<api-token>"
apps-platform app deploy
```

The app reads it back at `projects/$PROJECT_ID/secrets/master-checklist-confluence-token/versions/latest`
via the Secret Manager client — see `secrets.go`.

## Access control

This app should be restricted to Applied FTEs and the `ext-frontier` external group.
That restriction is configured at the `apps-platform` project level (not in this app's
code) — set it up in the apps-platform project settings after the first deploy.
