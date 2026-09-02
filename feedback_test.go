package main

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestContainsJapanese(t *testing.T) {
	cases := map[string]bool{
		"車両の起動に失敗しました":            true, // kanji + hiragana
		"エラーが出ました":                true, // katakana
		"Run 801 failed to start": false,
		"":                        false,
		"mixed: ログを確認してください (see run 12)": true,
	}
	for in, want := range cases {
		if got := containsJapanese(in); got != want {
			t.Errorf("containsJapanese(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBuildFeedbackEmail(t *testing.T) {
	now := time.Date(2026, 9, 2, 17, 30, 0, 0, time.UTC)
	raw := string(buildFeedbackEmail(feedbackEmail{
		From: "tester@ext.applied.co", To: "john.pham@applied.co",
		Type: "bug", Subject: "起動失敗", Message: "車両の起動に失敗しました。\nログを確認してください。",
		TranslatedSubject: "Startup failure", TranslatedMessage: "The vehicle failed to start.\nPlease check the logs.",
		Lang: "ja", Page: "https://x/feedback", Environment: "experimental.apps.applied.dev", Revision: "master-checklist-00006-abc", Now: now,
	}))
	headers, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body separator in message:\n%s", raw)
	}
	for _, want := range []string{
		"From: tester@ext.applied.co\r\n",
		"To: john.pham@applied.co\r\n",
		"Content-Transfer-Encoding: base64\r\n",
		"X-Master-Checklist-Type: bug\r\n",
	} {
		if !strings.Contains(headers+"\r\n", want) {
			t.Errorf("headers missing %q:\n%s", want, headers)
		}
	}
	// Subject is RFC 2047 encoded but must decode to the English headline.
	subj := ""
	for _, h := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(h, "Subject: ") {
			subj = strings.TrimPrefix(h, "Subject: ")
		}
	}
	if !strings.Contains(subj, "Startup_failure") && !strings.Contains(subj, "Startup failure") {
		t.Errorf("subject does not carry the translated headline: %q", subj)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body, "\r\n", ""))
	if err != nil {
		t.Fatalf("body is not valid base64: %v", err)
	}
	text := string(decoded)
	for _, want := range []string{
		"Master Checklist — Bug report",
		"From:        tester@ext.applied.co",
		"Environment: experimental.apps.applied.dev master-checklist-00006-abc",
		"---- English translation (automatic) ----",
		"The vehicle failed to start.",
		"---- Original message ----",
		"車両の起動に失敗しました。",
		"(EN) Startup failure",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("body missing %q:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(body, "\r\n") {
		if len(line) > 76 {
			t.Errorf("base64 line longer than 76 chars: %d", len(line))
		}
	}
}

func TestBuildFeedbackEmailUntranslated(t *testing.T) {
	text := string(buildFeedbackEmail(feedbackEmail{
		To: "john.pham@applied.co", Type: "help", Message: "How do I file a candidate run?", Now: time.Now(),
	}))
	if !strings.Contains(text, "Subject: [Master Checklist] Help request: How do I file a candidate run?") &&
		!strings.Contains(text, "Subject: =?utf-8?q?") {
		t.Errorf("unexpected subject line in:\n%s", text)
	}
	if strings.Contains(text, "From: \r\n") {
		t.Error("empty From header must be omitted")
	}
}

func TestDataAPIErrorNeedsConnect(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusNotFound, `{"error":"no connection found for integration google-mail"}`, true},
		{http.StatusForbidden, `insufficient authentication scopes`, true},
		{http.StatusUnauthorized, ``, true},
		{http.StatusBadRequest, `{"error":{"message":"Invalid value for raw"}}`, false},
		{http.StatusInternalServerError, `upstream timeout`, false},
	}
	for _, c := range cases {
		e := &dataAPIError{Status: c.status, Body: c.body}
		if got := e.needsConnect(); got != c.want {
			t.Errorf("needsConnect(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
