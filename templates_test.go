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
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index template missing %q", want)
		}
	}

	buf.Reset()
	if err := confirmTemplate.Execute(&buf, struct {
		PageURL        string
		GatekeeperURL  string
		ShowGatekeeper bool
		AssetVersion   string
		FailedUploads  []string
	}{PageURL: "https://x/page", GatekeeperURL: gatekeeperURL, ShowGatekeeper: true, AssetVersion: assetVersion}); err != nil {
		t.Fatalf("confirm template: %v", err)
	}
	if !strings.Contains(buf.String(), `app.js?v=`+assetVersion) || !strings.Contains(buf.String(), "https://x/page") {
		t.Errorf("confirm template output unexpected:\n%s", buf.String())
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
