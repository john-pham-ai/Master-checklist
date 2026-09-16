package main

import (
	"bytes"
	"strings"
	"testing"
)

// Every page template must execute with the data its handler passes; a missing
// field would otherwise only surface as a 500 in production.
func TestTemplatesExecute(t *testing.T) {
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, formData{
		PreflightChecks: preflightChecks, EngagementChecks: engagementChecks, DisengagementChecks: disengagementChecks,
		Maneuvers: closedLoopManeuvers, ManeuverOutcomes: closedLoopOutcomes,
		Today: "2026-09-02", GithubURL: githubURL, CurrentEngineer: "John Pham",
		Vehicles: parseVehicleRange(defaultVehicleRange), AssetVersion: assetVersion,
	}); err != nil {
		t.Fatalf("index template: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		`app.js?v=` + assetVersion, `style.css?v=` + assetVersion,
		`<option value="master" selected`, `id="tag-select"`, `id="diff-base"`, `id="diff-card"`,
		`value="John Pham"`, `<option value="801">`, `<option value="835">`, `href="/feedback"`,
		// The truck fetch buttons are always rendered (the endpoint, not the
		// markup, explains that fetching only works locally).
		`truck-fetch-btn" data-field="run_id" data-notes="logs_recording"`,
		`truck-fetch-btn" data-field="disengagement_run_id"`,
		`truck-fetch-btn" data-field="closed_loop_run_id"`,
		`truck-status muted small" hidden`,
		`data-i18n="commit_hash_hint"`,
		// The setup lives only in its own collapsible card: no per-check
		// setup buttons, a remote-IP field, and the chevron summary rows.
		`<details class="card collapsible-card" id="truck-ssh-card"`,
		`<details class="card collapsible-card" id="diff-card"`,
		`id="truck-setup-btn"`, `id="truck-setup-vehicle"`, `id="truck-setup-remote-ip"`,
		`id="truck-remote-hint"`,
		`data-i18n="truck_setup_remote_ip"`,
		// The Master closed loop GO approval sub-section lives inside the
		// Closed Loop card; hidden by default, shown by app.js for Master.
		`id="closed-loop-approval"`, `id="closed-loop-approval-link"`,
		`name="closed_loop_approval_link"`, `name="closed_loop_approval_message"`,
		`id="copy-approval-link-btn"`, `id="fetch-approval-message-btn"`,
		`data-i18n="section_closed_loop_approval"`,
		// Closed loop maneuvers: one checkbox per maneuver, four outcome
		// buttons and a notes field under each.
		`name="cl_maneuver_cut_in"`, `name="cl_maneuver_lane_change"`, `name="cl_maneuver_stop_lead_vehicle"`,
		`name="cl_maneuver_result_cut_in" value="comfortable"`, `name="cl_maneuver_result_cut_in" value="too_late"`,
		`name="cl_maneuver_result_cut_in" value="unable"`, `name="cl_maneuver_result_cut_in" value="jerky"`,
		`name="cl_maneuver_notes_stop_lead_vehicle"`,
		// The Lichtblick check ships a reference screenshot in its help.
		`src="/static/img/lichtblick-sensor-validation.jpg"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index template missing %q", want)
		}
	}
	if n := strings.Count(html, `data-i18n="truck_setup_btn"`); n != 1 {
		t.Errorf("truck_setup_btn rendered %d times, want exactly 1 (the setup card only)", n)
	}
	// The free-text Maneuvers input was replaced by the checkbox list.
	if strings.Contains(html, `name="closed_loop_maneuvers"`) {
		t.Errorf("legacy closed_loop_maneuvers text input still rendered")
	}
	// Only the Lichtblick check has an example image; exactly one is rendered.
	if n := strings.Count(html, `class="check-example"`); n != 1 {
		t.Errorf("check-example rendered %d times, want exactly 1", n)
	}
	// The example image must actually be embedded, or the page shows a
	// broken image.
	if _, err := staticFS.ReadFile("static/img/lichtblick-sensor-validation.jpg"); err != nil {
		t.Errorf("example screenshot not embedded: %v", err)
	}

	buf.Reset()
	if err := confirmTemplate.Execute(&buf, struct {
		PageURL        string
		GatekeeperURL  string
		ShowGatekeeper bool
		AssetVersion   string
		FailedUploads  []string
		EmptyUploads   []string
		RetryID        string
	}{PageURL: "https://x/page", GatekeeperURL: gatekeeperURL, ShowGatekeeper: true, AssetVersion: assetVersion, FailedUploads: []string{"syscheck-clip-1.webm"}, EmptyUploads: []string{"broken.png"}, RetryID: "retry-abc"}); err != nil {
		t.Fatalf("confirm template: %v", err)
	}
	confirmHTML := buf.String()
	if !strings.Contains(confirmHTML, `app.js?v=`+assetVersion) || !strings.Contains(confirmHTML, "https://x/page") {
		t.Errorf("confirm template output unexpected:\n%s", confirmHTML)
	}
	for _, want := range []string{"syscheck-clip-1.webm", "broken.png", "arrived with no content"} {
		if !strings.Contains(confirmHTML, want) {
			t.Errorf("confirm template missing %q (upload warnings)", want)
		}
	}
	// The failed-uploads warning offers a Retry button when the server held
	// the files.
	if !strings.Contains(confirmHTML, `id="retry-uploads-btn" data-retry-id="retry-abc"`) {
		t.Errorf("confirm template missing retry button:\n%s", confirmHTML)
	}

	// Without a RetryID (files too big to hold) the warning stays but the
	// button must not render.
	buf.Reset()
	if err := confirmTemplate.Execute(&buf, struct {
		PageURL        string
		GatekeeperURL  string
		ShowGatekeeper bool
		AssetVersion   string
		FailedUploads  []string
		EmptyUploads   []string
		RetryID        string
	}{PageURL: "https://x/page", GatekeeperURL: gatekeeperURL, ShowGatekeeper: true, AssetVersion: assetVersion, FailedUploads: []string{"syscheck-clip-1.webm"}}); err != nil {
		t.Fatalf("confirm template (no retry): %v", err)
	}
	if strings.Contains(buf.String(), "retry-uploads-btn") {
		t.Errorf("retry button rendered without a RetryID")
	}

	buf.Reset()
	if err := feedbackTemplate.Execute(&buf, struct {
		CurrentEmail string
		FeedbackTo   string
		AssetVersion string
	}{CurrentEmail: "john.pham@applied.co", FeedbackTo: defaultFeedbackTo, AssetVersion: assetVersion}); err != nil {
		t.Fatalf("feedback template: %v", err)
	}
	for _, want := range []string{`feedback.js?v=` + assetVersion, defaultFeedbackTo, `id="feedback-form"`, `id="lang-select"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("feedback template missing %q", want)
		}
	}
}

func TestAssetVersionIsStableAndNonEmpty(t *testing.T) {
	if assetVersion == "" || len(assetVersion) < 8 {
		t.Fatalf("assetVersion = %q", assetVersion)
	}
	if computeAssetVersion() != assetVersion {
		t.Error("assetVersion must be deterministic for the same embedded files")
	}
}
