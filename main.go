package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/john-pham-ai/Master-checklist/confluence"
)

//go:embed templates/index.html.tmpl
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed i18n
var i18nFS embed.FS

var pageTemplate = template.Must(template.ParseFS(templatesFS, "templates/index.html.tmpl"))

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

		report := confluence.RunReport{
			Tag:           r.FormValue("tag"),
			Date:          r.FormValue("date"),
			Vehicle:       r.FormValue("vehicle"),
			TestEngineer:  r.FormValue("test_engineer"),
			CommitHash:    r.FormValue("commit_hash"),
			SlackThread:   r.FormValue("slack_thread"),
			OverallResult: r.FormValue("overall_result"),
			RunID:         r.FormValue("run_id"),

			Preflight:     collectChecks(r, preflightChecks),
			Engagement:    collectChecks(r, engagementChecks),
			Disengagement: collectChecks(r, disengagementChecks),
		}

		monthTitle := monthTitleFromDate(report.Date)
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
		client := confluence.NewClient(cfg.BaseURL, cfg.SpaceKey, cfg.ParentPageID, cfg.BotEmail, token)

		monthPageID, err := client.FindOrCreateMonthPage(monthTitle)
		if err != nil {
			log.Printf("FindOrCreateMonthPage error: %v", err)
			http.Error(w, "failed to reach Confluence", http.StatusBadGateway)
			return
		}

		pageURL, err := client.CreateRunPage(monthPageID, title, body)
		if err != nil {
			log.Printf("CreateRunPage error: %v", err)
			http.Error(w, "failed to create Confluence page", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<p>Created: <a href="` + template.HTMLEscapeString(pageURL) + `">` + template.HTMLEscapeString(pageURL) + `</a></p><p><a href="/">Back</a></p>`))
	}
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
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	mux.Handle("/i18n/", http.FileServer(http.FS(i18nFS)))

	log.Printf("listening on %s (dry_run=%v)", cfg.Addr, cfg.DryRun)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
