package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSlackPermalink(t *testing.T) {
	tests := []struct {
		name      string
		link      string
		channel   string
		ts        string
		threadTS  string
		wantError bool
	}{
		{
			name:     "thread reply permalink (real GO approval format)",
			link:     "https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789488051626809?thread_ts=1789474157.142449&cid=C0B1L8F6NUX",
			channel:  "C0B1L8F6NUX",
			ts:       "1789488051.626809",
			threadTS: "1789474157.142449",
		},
		{
			name:      "client url, not a permalink",
			link:      "https://app.slack.com/client/T123/C456",
			wantError: true,
		},
		{
			name:    "permalink without query",
			link:    "https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789488051626809",
			channel: "C0B1L8F6NUX",
			ts:      "1789488051.626809",
		},
		{
			name:    "named channel path form",
			link:    "https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/smoke-tests/p1789488051626809",
			channel: "C0B1L8F6NUX",
			ts:      "1789488051.626809",
		},
		{
			name:    "thread_ts equal to message ts means the link is the thread root",
			link:    "https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789474157142449?thread_ts=1789474157.142449",
			channel: "C0B1L8F6NUX",
			ts:      "1789474157.142449",
		},
		{
			name:      "not a slack link",
			link:      "https://frontier.prod.applied.dev/cloud_engine/results/test/275233",
			wantError: true,
		},
		{
			name:      "empty",
			link:      "",
			wantError: true,
		},
		{
			name:      "short message id",
			link:      "https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p12345",
			wantError: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			channel, ts, threadTS, err := parseSlackPermalink(tc.link)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parseSlackPermalink(%q) = %q, %q, %q; want error", tc.link, channel, ts, threadTS)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSlackPermalink(%q): %v", tc.link, err)
			}
			if channel != tc.channel || ts != tc.ts || threadTS != tc.threadTS {
				t.Errorf("parseSlackPermalink(%q) = %q, %q, %q; want %q, %q, %q",
					tc.link, channel, ts, threadTS, tc.channel, tc.ts, tc.threadTS)
			}
		})
	}
}

// fakeSlackAPI emulates the two Web API methods fetchSlackMessage uses. Each
// map key is "method|expected param=value" verified by the test.
func fakeSlackAPI(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		key := r.URL.Path + "?" + r.PostForm.Encode()
		body, ok := responses[key]
		if !ok {
			t.Errorf("unexpected request %q", key)
			w.Write([]byte(`{"ok":false,"error":"not_found_in_fake"}`))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		w.Write([]byte(body))
	}))
}

func TestFetchSlackMessageThreadReply(t *testing.T) {
	srv := fakeSlackAPI(t, map[string]string{
		"/conversations.replies?channel=C0B1L8F6NUX&limit=200&ts=1789474157.142449": `{"ok":true,"messages":[` +
			`{"ts":"1789474157.142449","text":"parent"},{"ts":"1789474157.363729","text":"failing scenarios"},` +
			`{"ts":"1789488051.626809","text":"Recommended decision: GO"}]}`,
	})
	defer srv.Close()

	text, err := fetchSlackMessage(context.Background(), srv.Client(), srv.URL, "test-token",
		"C0B1L8F6NUX", "1789488051.626809", "1789474157.142449")
	if err != nil {
		t.Fatalf("fetchSlackMessage: %v", err)
	}
	if text != "Recommended decision: GO" {
		t.Errorf("text = %q", text)
	}
}

func TestFetchSlackMessageChannelRoot(t *testing.T) {
	srv := fakeSlackAPI(t, map[string]string{
		"/conversations.history?channel=C0B1L8F6NUX&inclusive=true&latest=1789474157.142449&limit=1": `{"ok":true,"messages":[` +
			`{"ts":"1789474157.142449","text":"Frontier Master <=> Master Sim Results"}]}`,
	})
	defer srv.Close()

	text, err := fetchSlackMessage(context.Background(), srv.Client(), srv.URL, "test-token",
		"C0B1L8F6NUX", "1789474157.142449", "")
	if err != nil {
		t.Fatalf("fetchSlackMessage: %v", err)
	}
	if text != "Frontier Master <=> Master Sim Results" {
		t.Errorf("text = %q", text)
	}
}

func TestFetchSlackMessageErrors(t *testing.T) {
	t.Run("slack error envelope", func(t *testing.T) {
		srv := fakeSlackAPI(t, map[string]string{
			"/conversations.history?channel=CSECRET&inclusive=true&latest=1789474157.142449&limit=1": `{"ok":false,"error":"channel_not_found"}`,
		})
		defer srv.Close()
		if _, err := fetchSlackMessage(context.Background(), srv.Client(), srv.URL, "test-token",
			"CSECRET", "1789474157.142449", ""); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
			t.Errorf("err = %v, want channel_not_found", err)
		}
	})
	t.Run("message not in thread", func(t *testing.T) {
		srv := fakeSlackAPI(t, map[string]string{
			"/conversations.replies?channel=C0B1L8F6NUX&limit=200&ts=1789474157.142449": `{"ok":true,"messages":[{"ts":"1789474157.142449","text":"only the parent"}]}`,
		})
		defer srv.Close()
		if _, err := fetchSlackMessage(context.Background(), srv.Client(), srv.URL, "test-token",
			"C0B1L8F6NUX", "1789488051.626809", "1789474157.142449"); err == nil {
			t.Error("expected error for missing reply")
		}
	})
}

func TestSlackMessageHandlerDryRun(t *testing.T) {
	// In dry-run the handler answers without hitting Slack at all, so the
	// approval UI can be exercised locally.
	srv := httptest.NewServer(makeSlackMessageHandler(config{DryRun: true}))
	defer srv.Close()

	link := "https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789488051626809?thread_ts=1789474157.142449"
	resp, err := srv.Client().Get(srv.URL + "/api/slack/message?link=" + link)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "GO") {
		t.Errorf("dry-run text = %q", out.Text)
	}

	// A non-permalink is still rejected even in dry run.
	resp2, err := srv.Client().Get(srv.URL + "/api/slack/message?link=https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("status for bad link = %d, want 400", resp2.StatusCode)
	}
}
