package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// slackAPIBase is the production Slack Web API root; fetchSlackMessage takes
// the base as a parameter so tests can point it at an httptest server.
// Org-level (Enterprise Grid) bot tokens work against the same host as
// regular workspace tokens.
const slackAPIBase = "https://www.slack.com/api"

// slackHTTPTimeout bounds a single Slack API call from the /api/slack/message
// endpoint, which the browser awaits while filling the approval message.
const slackHTTPTimeout = 15 * time.Second

// slackPermalinkRE matches Slack message permalinks like
//
//	https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789488051626809?thread_ts=1789474157.142449&cid=C0B1L8F6NUX
//
// The message id "p1789488051626809" packs the message timestamp as 10 digits
// of seconds followed by 6 digits of microseconds. An optional team/channel
// name segment before the p-id is also accepted (…/archives/C123/team/p456…).
var slackPermalinkRE = regexp.MustCompile(`/archives/([A-Z0-9]+)/(?:[A-Za-z0-9._-]+/)?p(\d{16})(?:[?#].*)?$`)

// parseSlackPermalink extracts the channel id, message ts and (when the link
// points into a thread) the parent thread ts from a Slack permalink.
func parseSlackPermalink(link string) (channel, ts, threadTS string, err error) {
	link = strings.TrimSpace(link)
	m := slackPermalinkRE.FindStringSubmatch(link)
	if m == nil {
		return "", "", "", fmt.Errorf("not a Slack message permalink (expected https://…/archives/C…/p…): %s", link)
	}
	channel = m[1]
	ts = m[2][:10] + "." + m[2][10:]

	if u, uerr := url.Parse(link); uerr == nil {
		if tt := u.Query().Get("thread_ts"); tt != "" && tt != ts {
			threadTS = tt
		}
	}
	return channel, ts, threadTS, nil
}

// slackAPIResponse is the envelope every Slack Web API method returns; ok is
// false and error carries the Slack error code on failure.
type slackAPIResponse struct {
	OK       bool       `json:"ok"`
	Error    string     `json:"error"`
	Messages []slackMsg `json:"messages"`
}

type slackMsg struct {
	TS   string `json:"ts"`
	Text string `json:"text"`
}

// slackPostForm calls one Slack Web API method with the bot token and decodes
// the standard ok/error envelope.
func slackPostForm(ctx context.Context, client *http.Client, apiBase, endpoint, token string, form url.Values) (slackAPIResponse, error) {
	var out slackAPIResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("slack api %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("slack api %s: decode response: %w", endpoint, err)
	}
	if !out.OK {
		return out, fmt.Errorf("slack api %s: %s", endpoint, out.Error)
	}
	return out, nil
}

// fetchSlackMessage returns the text of the single message a permalink points
// at. A channel-root message is fetched with conversations.history(latest=ts);
// a reply inside a thread is fetched with conversations.replies on the parent
// thread and then matched by ts. Threads longer than 200 replies (or messages
// more than 200 messages back) are not paged through — the tester can always
// paste the message text manually.
func fetchSlackMessage(ctx context.Context, client *http.Client, apiBase, token, channel, ts, threadTS string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: slackHTTPTimeout}
	}

	var (
		resp slackAPIResponse
		err  error
	)
	if threadTS != "" {
		resp, err = slackPostForm(ctx, client, apiBase, "/conversations.replies", token, url.Values{
			"channel": {channel},
			"ts":      {threadTS},
			"limit":   {"200"},
		})
	} else {
		resp, err = slackPostForm(ctx, client, apiBase, "/conversations.history", token, url.Values{
			"channel":   {channel},
			"latest":    {ts},
			"inclusive": {"true"},
			"limit":     {"1"},
		})
	}
	if err != nil {
		return "", err
	}
	for _, msg := range resp.Messages {
		if msg.TS == ts {
			return msg.Text, nil
		}
	}
	return "", fmt.Errorf("message %s not found in %s (is the bot a member of the channel?)", ts, channel)
}

// dryRunSlackMessage lets the approval UI be exercised locally (and in
// tests) without a real Slack bot token. It mirrors the shape of the real GO
// decision messages posted in the master smoke test channel.
const dryRunSlackMessage = "Recommended decision: *GO* :large_green_circle:\n" +
	"• 1 regression and 2 progressions.\n" +
	"Known issues are tracked in open Jira tickets."

// makeSlackMessageHandler serves GET /api/slack/message?link=<permalink> and
// returns {"text": …} for the message the permalink points at. Without a
// slack-bot-token secret configured (and outside dry-run), it fails with 503
// so the frontend can tell the tester to paste the message manually.
func makeSlackMessageHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		channel, ts, threadTS, err := parseSlackPermalink(r.URL.Query().Get("link"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if cfg.DryRun {
			json.NewEncoder(w).Encode(map[string]string{"text": dryRunSlackMessage})
			return
		}

		token, err := cfg.SlackToken.Get(r.Context())
		if err != nil {
			log.Printf("failed to load slack bot token: %v", err)
			http.Error(w, "Slack bot token is not configured", http.StatusServiceUnavailable)
			return
		}
		text, err := fetchSlackMessage(r.Context(), nil, slackAPIBase, token, channel, ts, threadTS)
		if err != nil {
			log.Printf("fetchSlackMessage %s: %v", channel, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"text": text})
	}
}
