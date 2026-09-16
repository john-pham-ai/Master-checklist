package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryStorePutGetEvict(t *testing.T) {
	s := newRetryStore()
	files := func(prefix string, size int) []retryFile {
		return []retryFile{{PageFilename: prefix + ".png", ContentType: "image/png", Data: bytes.Repeat([]byte{1}, size)}}
	}

	if !s.put("a", "p1", files("a", 1000)) {
		t.Fatal("put a rejected")
	}
	if !s.put("b", "p2", files("b", 1000)) {
		t.Fatal("put b rejected")
	}
	if got := s.get("a"); got == nil || got.PageID != "p1" || len(got.Files) != 1 {
		t.Fatalf("get a = %+v", got)
	}
	if got := s.get("missing"); got != nil {
		t.Fatalf("get missing = %+v, want nil", got)
	}

	// Oversized packages are refused (no Retry button for huge recordings).
	if s.put("big", "p3", []retryFile{{Data: make([]byte, maxRetryBytes+1)}}) {
		t.Error("oversized package accepted")
	}
	if got := s.get("big"); got != nil {
		t.Error("refused package was stored anyway")
	}

	// update() shrinks the package to the still-failing files.
	s.update("a", []retryFile{{PageFilename: "a2.png", ContentType: "image/png", Data: make([]byte, 10)}})
	if got := s.get("a"); len(got.Files) != 1 || got.Files[0].PageFilename != "a2.png" {
		t.Fatalf("after update: %+v", got.Files)
	}

	s.delete("b")
	if got := s.get("b"); got != nil {
		t.Error("deleted package still present")
	}
}

func TestRetryStoreTTLAndEviction(t *testing.T) {
	s := newRetryStore()
	small := []retryFile{{PageFilename: "f.png", Data: make([]byte, 10)}}

	// Expired packages disappear even without new activity.
	if !s.put("old", "p1", small) {
		t.Fatal("put rejected")
	}
	s.mu.Lock()
	s.packages["old"].Created = time.Now().Add(-retryTTL - time.Minute)
	s.mu.Unlock()
	if got := s.get("old"); got != nil {
		t.Error("expired package still returned")
	}

	// The oldest package is evicted when too many are held.
	for i := 0; i < maxRetryPackages; i++ {
		if !s.put(fmt.Sprintf("p%d", i), "page", small) {
			t.Fatalf("put p%d rejected", i)
		}
	}
	if got := s.get("p0"); got != nil {
		t.Log("p0 may still exist (capacity) — the next put must evict it")
	}
	if !s.put("newest", "page", small) {
		t.Fatal("put newest rejected")
	}
	if got := s.get("p0"); got != nil {
		t.Error("oldest package not evicted beyond maxRetryPackages")
	}
	if got := s.get("newest"); got == nil {
		t.Error("newest package missing")
	}
}

// fakeConfluence emulates just enough of the Confluence REST API for the
// submit + retry flow: space lookup, page listing/creation, and attachments
// that fail until `failAttachments` swaps to 0.
type fakeConfluence struct {
	uploads         atomic.Int32 // how many attachment uploads should still fail
	attachmentCalls atomic.Int32
}

func (f *fakeConfluence) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v2/spaces"):
		w.Write([]byte(`{"results":[{"id":"1"}]}`))
	case strings.HasPrefix(r.URL.Path, "/api/v2/pages") && r.Method == http.MethodGet:
		w.Write([]byte(`{"results":[]}`))
	case strings.HasPrefix(r.URL.Path, "/api/v2/pages"):
		w.Write([]byte(`{"id":"p1","_links":{"webui":"/x"}}`))
	case strings.Contains(r.URL.Path, "/child/attachment"):
		f.attachmentCalls.Add(1)
		if f.uploads.Load() > 0 {
			f.uploads.Add(-1)
			http.Error(w, "temporary confluence hiccup", http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"results":[{"title":"ok"}]}`))
	default:
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}
}

// TestSubmitToRetryE2E runs the whole flow against a flaky fake Confluence:
// the submit succeeds with a warning, and the confirmation page's Retry
// endpoint re-uploads the held files and clears the failure.
func TestSubmitToRetryE2E(t *testing.T) {
	fake := &fakeConfluence{}
	fake.uploads.Store(1) // first attachment upload fails, the retry succeeds
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cfg := loadConfig()
	cfg.BaseURL = srv.URL
	cfg.DryRun = false
	cfg.Token = newTokenSource("confluence-token", "CONFLUENCE_TOKEN", true) // dry-run token source: no Secret Manager
	cfg.SlackToken = newTokenSource("slack-bot-token", "SLACK_BOT_TOKEN", true)
	retries := newRetryStore()

	handler := makeSubmitHandler(cfg, retries)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{
		"test_type": "master", "tag": "t", "date": "2026-09-16", "vehicle": "810",
		"test_engineer": "e", "commit_hash": "c", "run_id": "r1", "overall_result": "pass",
	} {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreateFormFile("media_screenshot_syscheck", "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("pngdata")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/submit", &buf)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+mw.Boundary())
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d", rec.Code)
	}
	page := rec.Body.String()
	if !strings.Contains(page, "syscheck-screenshot-1.png") || !strings.Contains(page, "failed to upload") {
		t.Fatalf("confirmation page missing upload-failure warning:\n%s", page)
	}
	if !strings.Contains(page, `id="retry-uploads-btn" data-retry-id="`) {
		t.Fatalf("confirmation page missing retry button:\n%s", page)
	}

	// Extract the retry id and call the retry endpoint.
	idStart := strings.Index(page, `data-retry-id="`) + len(`data-retry-id="`)
	idEnd := strings.Index(page[idStart:], `"`)
	retryID := page[idStart : idStart+idEnd]

	retryHandler := makeRetryUploadsHandler(cfg, retries)
	rreq := httptest.NewRequest(http.MethodPost, "/api/retry_uploads?id="+retryID, nil)
	rrec := httptest.NewRecorder()
	retryHandler(rrec, rreq)
	if rrec.Code != http.StatusOK {
		t.Fatalf("retry: %d", rrec.Code)
	}
	var out struct {
		OK     bool     `json:"ok"`
		Failed []string `json:"failed"`
	}
	if err := json.NewDecoder(rrec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || len(out.Failed) != 0 {
		t.Fatalf("retry result: %+v", out)
	}
	if got := fake.attachmentCalls.Load(); got != 2 {
		t.Errorf("attachment calls = %d, want 2 (1 failed + 1 retry)", got)
	}
	// After a successful retry the package is gone.
	if got := retries.get(retryID); got != nil {
		t.Error("retry package still present after success")
	}

	// A second retry call reports unavailable (id no longer held).
	rreq2 := httptest.NewRequest(http.MethodPost, "/api/retry_uploads?id="+retryID, nil)
	rrec2 := httptest.NewRecorder()
	retryHandler(rrec2, rreq2)
	var out2 struct {
		Unavailable bool `json:"unavailable"`
	}
	if err := json.NewDecoder(rrec2.Body).Decode(&out2); err != nil {
		t.Fatal(err)
	}
	if !out2.Unavailable {
		t.Errorf("second retry: %+v, want unavailable", out2)
	}
}
