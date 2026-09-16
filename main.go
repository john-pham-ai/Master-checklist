package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/john-pham-ai/Master-checklist/confluence"
)

//go:embed templates/index.html.tmpl templates/confirm.html.tmpl templates/feedback.html.tmpl
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed i18n
var i18nFS embed.FS

var pageTemplate = template.Must(template.ParseFS(templatesFS, "templates/index.html.tmpl"))
var confirmTemplate = template.Must(template.ParseFS(templatesFS, "templates/confirm.html.tmpl"))
var feedbackTemplate = template.Must(template.ParseFS(templatesFS, "templates/feedback.html.tmpl"))

// assetVersion fingerprints the embedded static/i18n files. Templates append it
// as ?v=… to asset URLs and the static handler uses it as the ETag, so a new
// deploy can never be served with a browser-cached app.js from the previous one
// (embed.FS has no modification times, so browsers otherwise guess).
var assetVersion = computeAssetVersion()

func computeAssetVersion() string {
	h := fnv.New64a()
	for _, fsys := range []fs.FS{staticFS, i18nFS} {
		fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, _ := fs.ReadFile(fsys, p)
			h.Write([]byte(p))
			h.Write(b)
			return nil
		})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// noCache serves embedded assets with revalidation on every load: browsers
// keep the bytes but must check the ETag, which changes with every deploy.
func noCache(h http.Handler) http.Handler {
	etag := `"` + assetVersion + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.ServeHTTP(w, r)
	})
}

const gatekeeperURL = "https://gatekeeper.experimental.apps.applied.dev/master-verification/trucking/new"

// maxSubmitBodyBytes bounds a whole /submit request (checklist fields plus
// every pasted screenshot and recorded/attached clip). Screen recordings are
// the largest thing this form accepts, hence the generous headroom.
const maxSubmitBodyMB int64 = 500
const maxSubmitBodyBytes = maxSubmitBodyMB << 20

// checkSpec describes one checklist line item rendered on the form. Verify/
// Pass/Fail are English fallback text rendered directly into the page (so it
// is never blank before JS loads); the same content is translatable via
// i18n keys "check_<Key>_verify" / "_pass" / "_fail" in i18n/*.json.
type checkSpec struct {
	Key    string
	Label  string
	Verify string // how to perform this check
	Pass   string // what a pass looks like
	Fail   string // what a fail looks like
	// Example is an optional path (under /static/) to a reference screenshot
	// shown inside the check's "How to verify" help, e.g. what the Lichtblick
	// camera grid should look like.
	Example string
}

var preflightChecks = []checkSpec{
	{Key: "syscheck", Label: "run_syscheck results",
		Verify: "Run `run_syscheck` on the vehicle and review its output in the terminal or log.",
		Pass:   "All subsystems report OK/green with no errors or warnings flagged.",
		Fail:   "Any subsystem reports an error, a timeout, or is missing from the output."},
	{Key: "timesync", Label: "check_timesync results",
		Verify: "Run `check_timesync` and review the reported clock offset for every ECU/sensor.",
		Pass:   "All components report a synchronized clock within the tool's tolerance threshold.",
		Fail:   "Any component reports clock drift beyond tolerance, or fails to report at all."},
	{Key: "build_launch", Label: "Software build and launch",
		Verify: "Confirm the target build is flashed and the AD stack launches cleanly; check the launch logs and process list.",
		Pass:   "The running build matches the intended Tag/commit hash and every process starts with no crash or restart loop.",
		Fail:   "The build does not match the intended Tag/commit hash, or any process crashes or fails to launch."},
	{Key: "health_monitor", Label: "Health monitor is healthy",
		Verify: "Open the health monitor dashboard and observe system status at idle and during a short drive.",
		Pass:   "Every component shows healthy/green status for the whole check with no persistent warnings or errors.",
		Fail:   "Any component shows a persistent warning or error, or drops out during the check."},
	{Key: "logs_recording", Label: "Logs recording in /media/hotswap1/frontier/",
		Verify: "After a short drive, check /media/hotswap1/frontier/ for log files created during this run.",
		Pass:   "New log files appear, are actively growing in size, and their timestamps match the run.",
		Fail:   "No new log files appear, files are empty or truncated, or timestamps don't match the run."},
}

var engagementChecks = []checkSpec{
	{Key: "engagement", Label: "Engagement checks",
		Verify: "Engage autonomy mode using the standard procedure and observe the takeover.",
		Pass:   "AD engages on the first attempt, the correct indicators/alerts fire, and control transitions cleanly to the vehicle.",
		Fail:   "Engagement fails, needs multiple attempts, throws an error, or the control transition is abrupt or unsafe."},
	{Key: "lichtblick_cameras", Label: "Lichtblick: cameras in the Sensors validation tab",
		Verify:  "Open Lichtblick, go to the Sensors Validation tab, then Cameras → Cameras Raw, and confirm every camera tile is displaying properly (see the example below).",
		Pass:    "All camera tiles show a live, correctly oriented image with no freezes, black tiles, or visual artifacts.",
		Fail:    "Any camera tile is black, frozen, missing, rotated/misaligned, or showing artifacts.",
		Example: "img/lichtblick-sensor-validation.jpg"},
	{Key: "health_monitor_nodes", Label: "Health monitor GUI: red nodes",
		Verify: "Open the health monitor GUI and note down whether any nodes are shown red.",
		Pass:   "No red nodes — every node reports healthy/OK while AD is engaged.",
		Fail:   "One or more nodes are red; note down which nodes in the Notes field."},
}

var disengagementChecks = []checkSpec{
	{Key: "steering_left", Label: "Disengagement: steering left",
		Verify: "With AD engaged, turn the steering wheel left with enough force to trigger a disengagement.",
		Pass:   "AD disengages immediately with the correct alert/indicator, and the safety driver has full manual control.",
		Fail:   "AD does not disengage, disengages with a noticeable delay, or manual control is not fully restored."},
	{Key: "steering_right", Label: "Disengagement: steering right",
		Verify: "With AD engaged, turn the steering wheel right with enough force to trigger a disengagement.",
		Pass:   "AD disengages immediately with the correct alert/indicator, and the safety driver has full manual control.",
		Fail:   "AD does not disengage, disengages with a noticeable delay, or manual control is not fully restored."},
	{Key: "accel", Label: "Disengagement: accel",
		Verify: "With AD engaged, press the accelerator pedal to trigger a disengagement.",
		Pass:   "AD disengages immediately with the correct alert/indicator, and the safety driver has full manual control.",
		Fail:   "AD does not disengage, disengages with a noticeable delay, or manual control is not fully restored."},
	{Key: "brake", Label: "Disengagement: brake",
		Verify: "With AD engaged, press the brake pedal to trigger a disengagement.",
		Pass:   "AD disengages immediately with the correct alert/indicator, and the safety driver has full manual control.",
		Fail:   "AD does not disengage, disengages with a noticeable delay, or manual control is not fully restored."},
	{Key: "cruise_control", Label: "Disengagement: cruise control",
		Verify: "With AD engaged, tap the cruise control stalk/button to trigger a disengagement.",
		Pass:   "AD disengages immediately with the correct alert/indicator, and the safety driver has full manual control.",
		Fail:   "AD does not disengage, disengages with a noticeable delay, or manual control is not fully restored."},
	{Key: "e_stop", Label: "Disengagement: e-stop",
		Verify: "With AD engaged, activate the e-stop to trigger a disengagement.",
		Pass:   "The vehicle disengages and comes to a safe stop immediately, with the correct alert/indicator.",
		Fail:   "The e-stop does not disengage AD, the stop is delayed, or the vehicle does not come to a safe stop."},
	{Key: "ad_md_button", Label: "Disengagement: AD/MD button",
		Verify: "With AD engaged, press the AD/MD button to trigger a disengagement.",
		Pass:   "AD disengages immediately with the correct alert/indicator, and the safety driver has full manual control.",
		Fail:   "AD does not disengage, disengages with a noticeable delay, or manual control is not fully restored."},
}

// closedLoopChecks are the pass/fail line items of the optional Closed Loop
// section (shown only when the tester ticks the Closed Loop toggle). Empty for
// now — add {key, label, verify, pass, fail} rows here as the section gains
// checks.
var closedLoopChecks = []checkSpec{}

// labeled is a {Key, Label} pair used for the closed loop maneuver list and
// its outcome buttons. Label is the English fallback rendered into the page;
// i18n keys "maneuver_<Key>" / "outcome_<Key>" translate it.
type labeled struct {
	Key   string
	Label string
}

// closedLoopManeuvers are the maneuvers a tester can tick off in the Closed
// Loop section. Each ticked one gets an outcome (closedLoopOutcomes) and notes.
var closedLoopManeuvers = []labeled{
	{"cut_in", "Cut in"},
	{"lane_change", "Lane change"},
	{"stop_lead_vehicle", "Stopping for lead vehicle"},
}

// closedLoopOutcomes are the possible results of one maneuver, shown as
// buttons under each ticked maneuver.
var closedLoopOutcomes = []labeled{
	{"comfortable", "Stopped comfortably"},
	{"too_late", "Stopped too late"},
	{"unable", "Unable to complete maneuver"},
	{"jerky", "Too jerky"},
}

// collectManeuvers reads the ticked maneuvers (cl_maneuver_<key>) with their
// outcome (cl_maneuver_result_<key>) and notes (cl_maneuver_notes_<key>).
// Unticked maneuvers are left out entirely — the page only lists what was
// actually tested.
func collectManeuvers(r *http.Request) []confluence.Maneuver {
	var out []confluence.Maneuver
	for _, m := range closedLoopManeuvers {
		if r.FormValue("cl_maneuver_"+m.Key) == "" {
			continue
		}
		out = append(out, confluence.Maneuver{
			Key:     m.Key,
			Label:   m.Label,
			Outcome: r.FormValue("cl_maneuver_result_" + m.Key),
			Notes:   r.FormValue("cl_maneuver_notes_" + m.Key),
		})
	}
	return out
}

type formData struct {
	PreflightChecks     []checkSpec
	EngagementChecks    []checkSpec
	DisengagementChecks []checkSpec
	ClosedLoopChecks    []checkSpec
	Maneuvers           []labeled // closed loop maneuver checkboxes
	ManeuverOutcomes    []labeled // outcome buttons under each ticked maneuver
	Today               string
	GithubURL           string
	CurrentEngineer     string   // signed-in user's name (from IAP), pre-fills Test Engineer
	Vehicles            []string // Vehicle field autofill options, e.g. 801..835
	AssetVersion        string   // cache-busting token for /static and /i18n URLs
}

const githubURL = "https://github.com/john-pham-ai/Master-checklist"

func makeIndexHandler(vehicles []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := formData{
			PreflightChecks:     preflightChecks,
			EngagementChecks:    engagementChecks,
			DisengagementChecks: disengagementChecks,
			ClosedLoopChecks:    closedLoopChecks,
			Maneuvers:           closedLoopManeuvers,
			ManeuverOutcomes:    closedLoopOutcomes,
			Today:               time.Now().Format("2006-01-02"),
			GithubURL:           githubURL,
			CurrentEngineer:     currentEngineerName(r),
			Vehicles:            vehicles,
			AssetVersion:        assetVersion,
		}
		setTridentCookie(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageTemplate.Execute(w, data); err != nil {
			log.Printf("template execute error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

// pendingUpload is a screenshot or clip parsed from the multipart form,
// waiting to be uploaded as a Confluence attachment once the run page (and
// so its page ID) exists. PageFilename is the exact name baked into the
// check's media macro in the rendered page body (see collectChecks).
type pendingUpload struct {
	PageFilename string
	Header       *multipart.FileHeader
}

// attachmentFilename builds a unique, predictable attachment name for one
// check's Nth screenshot/clip, keeping the original extension when present
// (Confluence and browsers both use it to pick a renderer/thumbnail).
func attachmentFilename(checkKey, kind string, index int, originalName, fallbackExt string) string {
	ext := fallbackExt
	if dot := strings.LastIndex(originalName, "."); dot != -1 && dot < len(originalName)-1 {
		ext = strings.ToLower(originalName[dot+1:])
	}
	return fmt.Sprintf("%s-%s-%d.%s", checkKey, kind, index+1, ext)
}

// collectMedia reads one media group's pasted screenshots
// ("media_screenshot_<key>") and attached/recorded clips ("media_video_<key>")
// from the parsed multipart form. It returns the attachment refs (named as
// they will appear on the Confluence page), the matching files to upload, and
// the names of any parts that arrived with no content.
//
// Empty parts are skipped rather than attached: a browser can lose the bytes
// behind a File in a long-lived tab (the media.js pre-submit check blocks
// those client-side, but the server must not trust that) — attaching the name
// anyway would put a broken 0-byte file on the Confluence page (a real
// incident on the 2026-09-16 run page). The skipped names are surfaced on the
// confirmation page so nothing disappears silently.
func collectMedia(r *http.Request, key string) (refs []confluence.MediaRef, uploads []pendingUpload, empty []string) {
	if r.MultipartForm != nil {
		n := 0 // contiguous counter over the parts that are actually attached
		for _, fh := range r.MultipartForm.File["media_screenshot_"+key] {
			if fh.Size == 0 {
				log.Printf("submit: skipping empty screenshot part %q (check %q)", fh.Filename, key)
				empty = append(empty, fh.Filename)
				continue
			}
			name := attachmentFilename(key, "screenshot", n, fh.Filename, "png")
			refs = append(refs, confluence.MediaRef{Filename: name, Kind: "image"})
			uploads = append(uploads, pendingUpload{PageFilename: name, Header: fh})
			n++
		}
		n = 0
		for _, fh := range r.MultipartForm.File["media_video_"+key] {
			if fh.Size == 0 {
				log.Printf("submit: skipping empty clip part %q (check %q)", fh.Filename, key)
				empty = append(empty, fh.Filename)
				continue
			}
			name := attachmentFilename(key, "clip", n, fh.Filename, "webm")
			refs = append(refs, confluence.MediaRef{Filename: name, Kind: "video"})
			uploads = append(uploads, pendingUpload{PageFilename: name, Header: fh})
			n++
		}
	}
	return refs, uploads, empty
}

// collectChecks reads each check's radio/notes fields plus its media (see
// collectMedia) from the parsed multipart form. It returns the check results,
// the matching list of files still to be uploaded, and the names of any media
// parts that arrived empty (skipped; see collectMedia).
func collectChecks(r *http.Request, specs []checkSpec) (results []confluence.CheckResult, uploads []pendingUpload, empty []string) {
	results = make([]confluence.CheckResult, 0, len(specs))
	for _, spec := range specs {
		media, mediaUploads, emptyMedia := collectMedia(r, spec.Key)
		uploads = append(uploads, mediaUploads...)
		empty = append(empty, emptyMedia...)
		results = append(results, confluence.CheckResult{
			Key:    spec.Key,
			Label:  spec.Label,
			Result: r.FormValue("result_" + spec.Key),
			Notes:  r.FormValue("notes_" + spec.Key),
			Media:  media,
		})
	}
	return results, uploads, empty
}

// uploadPendingMedia attaches every pending screenshot/clip to the just-created
// page. Uploads happen after the page (and its body's media macros) already
// exist, so a failed upload never blocks the run page itself — it's logged
// and surfaced as a warning on the confirmation screen instead.
func uploadPendingMedia(client *confluence.Client, pageID string, uploads []pendingUpload) []string {
	var warnings []string
	for _, u := range uploads {
		f, err := u.Header.Open()
		if err != nil {
			log.Printf("failed to open uploaded file %q: %v", u.Header.Filename, err)
			warnings = append(warnings, u.PageFilename)
			continue
		}
		contentType := u.Header.Header.Get("Content-Type")
		err = client.UploadAttachment(pageID, u.PageFilename, contentType, f)
		f.Close()
		if err != nil {
			log.Printf("failed to upload attachment %q: %v", u.PageFilename, err)
			warnings = append(warnings, u.PageFilename)
		}
	}
	return warnings
}

func makeSubmitHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Screenshots/clips ride along as multipart parts; cap the whole
		// request so a runaway recording can't exhaust server memory/disk.
		r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, fmt.Sprintf("bad form (is it too large? limit is %dMB)", maxSubmitBodyMB), http.StatusBadRequest)
			return
		}

		testType := r.FormValue("test_type")
		isCandidate := testType == "candidate"

		preflightResults, preflightUploads, emptyPreflight := collectChecks(r, preflightChecks)
		engagementResults, engagementUploads, emptyEngagement := collectChecks(r, engagementChecks)
		disengagementResults, disengagementUploads, emptyDisengagement := collectChecks(r, disengagementChecks)
		pendingMedia := append(append(preflightUploads, engagementUploads...), disengagementUploads...)
		emptyUploads := append(append(emptyPreflight, emptyEngagement...), emptyDisengagement...)

		// Photos pasted into the "Notes on the changes" section ride along
		// as the "notes" media group (no check uses that key).
		notesMedia, notesUploads, emptyNotes := collectMedia(r, "notes")
		pendingMedia = append(pendingMedia, notesUploads...)
		emptyUploads = append(emptyUploads, emptyNotes...)

		report := confluence.RunReport{
			Tag:           r.FormValue("tag"),
			Date:          r.FormValue("date"),
			Vehicle:       r.FormValue("vehicle"),
			TestEngineer:  r.FormValue("test_engineer"),
			CommitHash:    r.FormValue("commit_hash"),
			SlackThread:   r.FormValue("slack_thread"),
			RecordingLink: r.FormValue("recording_link"),
			OverallResult: r.FormValue("overall_result"),
			RunID:         r.FormValue("run_id"),

			Preflight:     preflightResults,
			Engagement:    engagementResults,
			Disengagement: disengagementResults,

			DisengagementRunID: r.FormValue("disengagement_run_id"),

			NotesMedia: notesMedia,
		}

		// The Closed Loop section only exists on the form when the tester
		// ticked the toggle, so its fields are only collected then.
		if r.FormValue("closed_loop") != "" {
			closedLoopResults, closedLoopUploads, emptyClosedLoop := collectChecks(r, closedLoopChecks)
			pendingMedia = append(pendingMedia, closedLoopUploads...)
			emptyUploads = append(emptyUploads, emptyClosedLoop...)
			report.ClosedLoop = confluence.ClosedLoop{
				Enabled:   true,
				RunID:     r.FormValue("closed_loop_run_id"),
				Maneuvers: collectManeuvers(r),
				Route:     r.FormValue("closed_loop_route"),
				Recording: r.FormValue("closed_loop_recording"),
				Checks:    closedLoopResults,
			}
			// The GO approval sub-section is only rendered for Master
			// Testing; the browser disables (and never posts) its fields
			// otherwise, and the server double-checks here.
			if !isCandidate {
				report.ClosedLoop.ApprovalLink = r.FormValue("closed_loop_approval_link")
				report.ClosedLoop.ApprovalMessage = r.FormValue("closed_loop_approval_message")
			}
		}

		// The browser posts back the diff summary it rendered (JSON produced by
		// /api/diff) plus the tester's optional notes.
		if d, err := parseDiffJSON(r.FormValue("diff_json")); err != nil {
			log.Printf("ignoring invalid diff_json: %v", err)
		} else if d != nil {
			report.Diff = toConfluenceDiff(d, r.FormValue("diff_notes"))
		}

		parentPageID := cfg.ParentPageID
		if isCandidate {
			parentPageID = cfg.CandidateParentPageID
		}

		// Confluence page titles must be unique across the whole space, not just
		// under one parent, so Candidate's month page needs a distinct title
		// from Master's even though they land in different folders.
		monthTitle := monthTitleFromDate(report.Date)
		if isCandidate {
			monthTitle += " (Candidate Testing)"
		}
		body := confluence.RenderStorageFormat(report)
		title := confluence.PageTitle(report)

		if cfg.DryRun {
			log.Printf("[dry-run] would create page %q under month page %q (with %d screenshot/clip attachment(s))\n%s",
				title, monthTitle, len(pendingMedia), body)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<p>Dry run: page not actually created. See server logs for the rendered payload. <a href="/">Back</a></p>`))
			return
		}

		token, err := cfg.Token.Get(r.Context())
		if err != nil {
			log.Printf("failed to load Confluence token: %v", err)
			http.Error(w, "Confluence credentials are not configured yet", http.StatusServiceUnavailable)
			return
		}
		client := confluence.NewClient(cfg.BaseURL, cfg.SpaceKey, parentPageID, cfg.BotEmail, token)

		monthPageID, err := client.FindOrCreateMonthPage(monthTitle)
		if err != nil {
			log.Printf("FindOrCreateMonthPage error: %v", err)
			http.Error(w, confluenceErrorText("look up the month page", err), http.StatusBadGateway)
			return
		}

		pageID, pageURL, err := client.CreateRunPage(monthPageID, title, body)
		if err != nil {
			log.Printf("CreateRunPage error: %v", err)
			http.Error(w, confluenceErrorText("create the run page", err), http.StatusBadGateway)
			return
		}

		// The page already renders (with placeholder media macros) before its
		// screenshots/clips exist as attachments, so an upload failure here is
		// only a warning, never a reason to fail a submission that otherwise
		// succeeded — it's shown on the confirmation screen instead.
		failedUploads := uploadPendingMedia(client, pageID, pendingMedia)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := confirmTemplate.Execute(w, struct {
			PageURL        string
			GatekeeperURL  string
			ShowGatekeeper bool
			AssetVersion   string
			FailedUploads  []string
			EmptyUploads   []string
		}{PageURL: pageURL, GatekeeperURL: gatekeeperURL, ShowGatekeeper: !isCandidate, AssetVersion: assetVersion, FailedUploads: failedUploads, EmptyUploads: emptyUploads}); err != nil {
			log.Printf("confirm template execute error: %v", err)
		}
	}
}

// tagCache caches the full tag list (name + commit SHA) from GitHub for a
// short time, since the list only changes when a new tag is pushed and the
// datalist may be refreshed on every keystroke.
type tagCache struct {
	cfg config

	mu        sync.Mutex
	tags      []githubTag
	fetchedAt time.Time
}

const tagCacheTTL = 60 * time.Second

// tagInfo is one entry of the /api/tags response: a build and the commit it
// points at. The browser uses the SHA to auto-fill the Commit Hash field.
type tagInfo struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

func mkSampleTag(name, sha string) githubTag {
	var t githubTag
	t.Name = name
	t.Commit.SHA = sha
	return t
}

var dryRunSampleTags = []string{
	"trucking-scheduled-night-2026-09-01",
	"trucking-scheduled-night-2026-08-31",
	"trucking-scheduled-night-2026-08-30",
	"trucking-candidate-2026-08-26-01",
	"trucking-candidate-2026-08-26-00",
}

var dryRunSampleTagInfos = []githubTag{
	mkSampleTag("trucking-scheduled-night-2026-09-01", "b17c0ffee4e5e6a7b8c9d0e1f2a3b4c5d6e7f809"),
	mkSampleTag("trucking-scheduled-night-2026-08-31", "a16bf0fee4e5e6a7b8c9d0e1f2a3b4c5d6e7f708"),
	mkSampleTag("trucking-scheduled-night-2026-08-30", "915ae0fee4e5e6a7b8c9d0e1f2a3b4c5d6e7f607"),
	mkSampleTag("trucking-candidate-2026-08-26-01", "8149d0fee4e5e6a7b8c9d0e1f2a3b4c5d6e7f506"),
	mkSampleTag("trucking-candidate-2026-08-26-00", "7138c0fee4e5e6a7b8c9d0e1f2a3b4c5d6e7f405"),
}

// infos returns the cached tags with their commit SHAs.
func (c *tagCache) infos(ctx context.Context) ([]githubTag, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfg.DryRun {
		return dryRunSampleTagInfos, nil
	}

	if time.Since(c.fetchedAt) < tagCacheTTL && c.tags != nil {
		return c.tags, nil
	}

	token, err := c.cfg.GithubToken.Get(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := fetchGithubTags(c.cfg.GithubOwner, c.cfg.GithubRepo, token)
	if err != nil {
		return nil, err
	}

	c.tags = tags
	c.fetchedAt = time.Now()
	return tags, nil
}

// Get returns just the tag names (the diff service works with names).
func (c *tagCache) Get(ctx context.Context) ([]string, error) {
	tags, err := c.infos(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return names, nil
}

func makeTagsHandler(cache *tagCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags, err := cache.infos(r.Context())
		if err != nil {
			log.Printf("fetchGithubTags error: %v", err)
			http.Error(w, "failed to fetch tags", http.StatusBadGateway)
			return
		}

		filterWord := "scheduled-night"
		if r.URL.Query().Get("test_type") == "candidate" {
			filterWord = "candidate"
		}

		filtered := filterTags(tags, filterWord)
		out := make([]tagInfo, len(filtered))
		for i, t := range filtered {
			out[i] = tagInfo{Name: t.Name, SHA: t.Commit.SHA}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// toConfluenceDiff converts the API's diff summary into the renderer's type.
func toConfluenceDiff(d *diffSummary, notes string) *confluence.DiffSummary {
	out := &confluence.DiffSummary{
		Repo: d.Repo, Base: d.Base, Head: d.Head, BaseDate: d.BaseDate, HeadDate: d.HeadDate,
		CompareURL: d.CompareURL, TotalCommits: d.TotalCommits,
		OtherCount: d.OtherCount, OtherAutomated: d.OtherAutomated, Impact: d.Impact,
		Ignored: d.Ignored, OnTruck: d.OnTruck,
		AISummary: d.AISummary, Notes: strings.TrimSpace(notes),
	}
	if len(d.Simple) > 0 {
		out.Simple = map[string]confluence.SimpleLang{}
		for lang, s := range d.Simple {
			cs := confluence.SimpleLang{Overall: s.Overall, Areas: map[string]confluence.SimpleArea{}}
			for k, a := range s.Areas {
				cs.Areas[k] = confluence.SimpleArea{Sentence: a.Sentence, Bullets: a.Bullets}
			}
			out.Simple[lang] = cs
		}
	}
	for _, c := range d.Categories {
		cat := confluence.DiffCategory{Key: c.Key, Label: c.Label, Undescribed: c.Undescribed, UndescribedDriving: c.UndescribedDriving, Impact: c.Impact}
		conv := func(it diffCommit) confluence.DiffItem {
			return confluence.DiffItem{
				Title: it.Title, Headline: it.Headline, PlainTitle: it.PlainTitle, URL: it.URL, Summary: it.Summary,
				Excerpt: it.Excerpt, Note: it.Note, Kind: it.Kind, Jira: it.Jira, PR: it.PR, PRURL: it.PRURL, Tags: it.Tags,
				Impact: it.Impact, NeedsInfo: it.NeedsInfo, IsFix: it.IsFix, IsRevert: it.IsRevert,
				SHA: it.SHA, Origin: it.Origin, Dirs: it.Dirs, Files: it.Files,
			}
		}
		for _, it := range c.Items {
			cat.Items = append(cat.Items, conv(it))
		}
		for _, it := range c.Flagged {
			cat.Flagged = append(cat.Flagged, conv(it))
		}
		out.Categories = append(out.Categories, cat)
	}
	return out
}

// confluenceErrorText turns a client error into the message shown to the test
// engineer. Auth failures are called out explicitly because Confluence reports
// a bad API token as an anonymous caller (v1: 403 "cannot access Confluence",
// v2: 404 NOT_FOUND) rather than a 401, which otherwise reads like an outage.
func confluenceErrorText(action string, err error) string {
	msg := err.Error()
	if strings.Contains(msg, "already exists") {
		return "A Confluence page with this title already exists — the title is built from Tag, Date and Run ID, " +
			"so this exact run has already been filed. Change the Run ID (or delete the existing page) and submit again. " +
			"(" + msg + ")"
	}
	if strings.Contains(msg, "403") || strings.Contains(msg, "404") || strings.Contains(msg, "401") {
		return fmt.Sprintf("Confluence rejected the request while trying to %s (%s). "+
			"This usually means the confluence-token secret is not a valid Atlassian API token for CONFLUENCE_BOT_EMAIL, "+
			"or that account cannot access the NEURON space.", action, msg)
	}
	return fmt.Sprintf("Failed to %s in Confluence: %s", action, msg)
}

func monthTitleFromDate(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		t = time.Now()
	}
	return t.Format("January 2006")
}

func main() {
	cfg := loadConfig()

	tags := &tagCache{cfg: cfg}
	tr := newTranslator(cfg.ProjectID, cfg.DryRun)

	mux := http.NewServeMux()
	mux.HandleFunc("/", makeIndexHandler(parseVehicleRange(cfg.VehicleRange)))
	mux.HandleFunc("/submit", makeSubmitHandler(cfg))
	mux.HandleFunc("/api/tags", makeTagsHandler(tags))
	mux.HandleFunc("/api/diff", newDiffService(cfg, tags, tr).handle)
	mux.HandleFunc("/api/truck/run_id", makeTruckRunIDHandler(cfg))
	mux.HandleFunc("/api/truck/ssh_setup", makeTruckSSHSetupHandler(cfg))
	mux.HandleFunc("/api/truck/ssh_lookup", makeTruckIPLookupHandler(cfg))
	mux.HandleFunc("/api/engineers", makeEngineersHandler(newEngineerSource(cfg.EngineerGroups, cfg.DryRun)))
	mux.HandleFunc("/api/slack/message", makeSlackMessageHandler(cfg))

	feedback := &feedbackService{cfg: cfg, data: newDataAPI(), tr: tr}
	mux.HandleFunc("/feedback", feedback.handleForm)
	mux.HandleFunc("/api/feedback", feedback.handleSubmit)
	mux.HandleFunc("/api/feedback/connect", feedback.handleConnect)
	mux.Handle("/static/", noCache(http.FileServer(http.FS(staticFS))))
	mux.Handle("/i18n/", noCache(http.FileServer(http.FS(i18nFS))))

	log.Printf("listening on %s (dry_run=%v)", cfg.Addr, cfg.DryRun)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
