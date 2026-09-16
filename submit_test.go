package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// multipartSubmit builds a parsed /submit-style request carrying the given
// parts, mirroring what the browser posts (ParseMultipartForm run first, the
// way makeSubmitHandler does).
func multipartSubmit(t *testing.T, fields map[string]string, files [][3]interface{}) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		name := f[0].(string)
		filename := f[1].(string)
		content := f[2].(string)
		part, err := w.CreateFormFile(name, filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/submit", &buf)
	r.Header.Set("Content-Type", w.FormDataContentType())
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	return r
}

func TestCollectMediaKeepsAndNamesParts(t *testing.T) {
	r := multipartSubmit(t,
		map[string]string{"test_type": "master"},
		[][3]interface{}{
			{"media_screenshot_syscheck", "photo 2026-09-16 at 10.43.48.png", "\x89PNG data"},
			{"media_video_syscheck", "syscheck-screen-1789488051626.webm", "webm data"},
			{"media_screenshot_notes", "pasted-note.png", "note"},
		})

	refs, uploads, empty := collectMedia(r, "syscheck")
	if len(refs) != 2 || len(uploads) != 2 {
		t.Fatalf("syscheck: got %d refs, %d uploads, want 2/2", len(refs), len(uploads))
	}
	if refs[0].Filename != "syscheck-screenshot-1.png" || refs[0].Kind != "image" {
		t.Errorf("refs[0] = %+v", refs[0])
	}
	if refs[1].Filename != "syscheck-clip-1.webm" || refs[1].Kind != "video" {
		t.Errorf("refs[1] = %+v", refs[1])
	}
	if len(empty) != 0 {
		t.Errorf("empty = %v, want none", empty)
	}

	// The png extension from the original name is preserved in the attachment
	// name; a dotless original would fall back to png/webm.
	if !strings.HasSuffix(refs[0].Filename, ".png") {
		t.Errorf("screenshot name lost its extension: %s", refs[0].Filename)
	}
}

func TestCollectMediaSkipsEmptyPartsAndNumbersContiguously(t *testing.T) {
	// The exact shape of the 2026-09-16 incident: parts arrive with their
	// filenames but zero content. They must not be attached (that put broken
	// 0-byte files on the Confluence page), and the parts that do have
	// content must still number 1, 2, … contiguously.
	r := multipartSubmit(t,
		map[string]string{"test_type": "master"},
		[][3]interface{}{
			{"media_screenshot_syscheck", "broken-clipboard-file.png", ""}, // empty
			{"media_screenshot_syscheck", "good.png", "png"},               // real
			{"media_video_syscheck", "lost-recording.webm", ""},            // empty
			{"media_video_syscheck", "also-lost.webm", ""},                 // empty
			{"media_video_syscheck", "keep.webm", "webm"},                  // real
		})

	refs, uploads, empty := collectMedia(r, "syscheck")
	if len(refs) != 2 || len(uploads) != 2 {
		t.Fatalf("got %d refs, %d uploads, want 2/2 (empties skipped)", len(refs), len(uploads))
	}
	if refs[0].Filename != "syscheck-screenshot-1.png" {
		t.Errorf("refs[0] = %s, want syscheck-screenshot-1.png (contiguous numbering)", refs[0].Filename)
	}
	if refs[1].Filename != "syscheck-clip-1.webm" {
		t.Errorf("refs[1] = %s, want syscheck-clip-1.webm (contiguous numbering)", refs[1].Filename)
	}
	if len(empty) != 3 {
		t.Fatalf("empty = %v, want the 3 skipped names", empty)
	}
	for _, want := range []string{"broken-clipboard-file.png", "lost-recording.webm", "also-lost.webm"} {
		found := false
		for _, e := range empty {
			if e == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("empty = %v, missing %q", empty, want)
		}
	}
}

func TestCollectChecksPropagatesEmptyParts(t *testing.T) {
	r := multipartSubmit(t,
		map[string]string{
			"test_type":       "master",
			"result_syscheck": "fail",
			"notes_syscheck":  "two cameras black",
			"result_timesync": "pass",
		},
		[][3]interface{}{
			{"media_screenshot_syscheck", "gone.png", ""},
			{"media_screenshot_timesync", "kept.png", "png"},
		})

	results, uploads, empty := collectChecks(r, preflightChecks)
	if len(results) != len(preflightChecks) {
		t.Fatalf("got %d results, want %d", len(results), len(preflightChecks))
	}
	if len(uploads) != 1 {
		t.Fatalf("uploads = %d, want 1 (empty part skipped)", len(uploads))
	}
	if len(empty) != 1 || empty[0] != "gone.png" {
		t.Errorf("empty = %v, want [gone.png]", empty)
	}
	// The syscheck row must not carry a media ref for the dropped part: a ref
	// with no attachment would render a broken placeholder on the page.
	for _, res := range results {
		if res.Key == "syscheck" && len(res.Media) != 0 {
			t.Errorf("syscheck kept %d media refs for skipped parts", len(res.Media))
		}
	}
}
