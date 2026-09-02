package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/john-pham-ai/Master-checklist/confluence"
)

//go:embed templates/index.html.tmpl templates/confirm.html.tmpl
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed i18n
var i18nFS embed.FS

var pageTemplate = template.Must(template.ParseFS(templatesFS, "templates/index.html.tmpl"))
var confirmTemplate = template.Must(template.ParseFS(templatesFS, "templates/confirm.html.tmpl"))

const gatekeeperURL = "https://gatekeeper.experimental.apps.applied.dev/master-verification/trucking/new"

// checkSpec describes one checklist line item rendered on the form.
type checkSpec struct {
	Key   string
	Label string
}

var preflightChecks = []checkSpec{
	{"syscheck", "run_syscheck results"},
	{"timesync", "check_timesync results"},
	{"build_launch", "Software build and launch"},
	{"health_monitor", "Health monitor is healthy"},
	{"logs_recording", "Logs recording in /media/hotswap1/frontier/"},
}

var engagementChecks = []checkSpec{
	{"engagement", "Engagement checks"},
}

var disengagementChecks = []checkSpec{
	{"steering_left", "Disengagement: steering left"},
	{"steering_right", "Disengagement: steering right"},
	{"accel", "Disengagement: accel"},
	{"brake", "Disengagement: brake"},
	{"cruise_control", "Disengagement: cruise control"},
	{"e_stop", "Disengagement: e-stop"},
	{"ad_md_button", "Disengagement: AD/MD button"},
}

type formData struct {
	PreflightChecks     []checkSpec
	EngagementChecks    []checkSpec
	DisengagementChecks []checkSpec
	Today               string
	GithubURL           string
}

const githubURL = "https://github.com/john-pham-ai/Master-checklist"

func handleIndex(w http.ResponseWriter, r *http.Request) {
	data := formData{
		PreflightChecks:     preflightChecks,
		EngagementChecks:    engagementChecks,
		DisengagementChecks: disengagementChecks,
		Today:               time.Now().Format("2006-01-02"),
		GithubURL:           githubURL,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, data); err != nil {
		log.Printf("template execute error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func collectChecks(r *http.Request, specs []checkSpec) []confluence.CheckResult {
	results := make([]confluence.CheckResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, confluence.CheckResult{
			Key:    spec.Key,
			Label:  spec.Label,
			Result: r.FormValue("result_" + spec.Key),
			Notes:  r.FormValue("notes_" + spec.Key),
		})
	}
	return results
}

func makeSubmitHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		testType := r.FormValue("test_type")
		isCandidate := testType == "candidate"

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

			Preflight:     collectChecks(r, preflightChecks),
			Engagement:    collectChecks(r, engagementChecks),
			Disengagement: collectChecks(r, disengagementChecks),

			DisengagementRunID:           r.FormValue("disengagement_run_id"),
			DisengagementClosedLoopRunID: r.FormValue("disengagement_closed_loop_run_id"),
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
			log.Printf("[dry-run] would create page %q under month page %q\n%s", title, monthTitle, body)
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

		pageURL, err := client.CreateRunPage(monthPageID, title, body)
		if err != nil {
			log.Printf("CreateRunPage error: %v", err)
			http.Error(w, confluenceErrorText("create the run page", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := confirmTemplate.Execute(w, struct {
			PageURL        string
			GatekeeperURL  string
			ShowGatekeeper bool
		}{PageURL: pageURL, GatekeeperURL: gatekeeperURL, ShowGatekeeper: !isCandidate}); err != nil {
			log.Printf("confirm template execute error: %v", err)
		}
	}
}

// tagCache caches the full tag list from GitHub for a short time, since the
// list only changes when a new tag is pushed and the datalist may be
// refreshed on every keystroke.
type tagCache struct {
	cfg config

	mu        sync.Mutex
	tags      []string
	fetchedAt time.Time
}

const tagCacheTTL = 60 * time.Second

var dryRunSampleTags = []string{
	"v1.42.0-scheduled-night-2026-08-31",
	"v1.41.0-scheduled-night-2026-08-30",
	"v1.42.0-candidate-1",
	"v1.42.0-candidate-2",
}

func (c *tagCache) Get(r *http.Request) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfg.DryRun {
		return dryRunSampleTags, nil
	}

	if time.Since(c.fetchedAt) < tagCacheTTL && c.tags != nil {
		return c.tags, nil
	}

	token, err := c.cfg.GithubToken.Get(r.Context())
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

func makeTagsHandler(cfg config) http.HandlerFunc {
	cache := &tagCache{cfg: cfg}
	return func(w http.ResponseWriter, r *http.Request) {
		tags, err := cache.Get(r)
		if err != nil {
			log.Printf("fetchGithubTags error: %v", err)
			http.Error(w, "failed to fetch tags", http.StatusBadGateway)
			return
		}

		filterWord := "scheduled-night"
		if r.URL.Query().Get("test_type") == "candidate" {
			filterWord = "candidate"
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(filterTags(tags, filterWord))
	}
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

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/submit", makeSubmitHandler(cfg))
	mux.HandleFunc("/api/tags", makeTagsHandler(cfg))
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	mux.Handle("/i18n/", http.FileServer(http.FS(i18nFS)))

	log.Printf("listening on %s (dry_run=%v)", cfg.Addr, cfg.DryRun)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
