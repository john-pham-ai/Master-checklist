package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/oauth2/google"
)

// Translation uses Gemini on Vertex AI (already enabled in the apps projects)
// through the app's service account. It requires roles/aiplatform.user on
// master-checklist-sa; until that is granted every call fails with 403 and the
// feedback email simply carries the original text plus a note.
const (
	defaultTranslateModel = "gemini-2.5-flash"
	defaultVertexLocation = "us-central1"
	vertexScope           = "https://www.googleapis.com/auth/cloud-platform"
)

type translator struct {
	project  string
	location string
	model    string
	disabled bool

	mu     sync.Mutex
	client *http.Client
}

func newTranslator(project string, dryRun bool) *translator {
	return &translator{
		project:  project,
		location: envOrDefault("VERTEX_LOCATION", defaultVertexLocation),
		model:    envOrDefault("TRANSLATE_MODEL", defaultTranslateModel),
		disabled: dryRun || project == "" || os.Getenv("TRANSLATE_DISABLED") == "true",
	}
}

// containsJapanese reports whether s contains Hiragana, Katakana or Han
// (kanji) characters.
func containsJapanese(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func (t *translator) httpClient() (*http.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		return t.client, nil
	}
	c, err := google.DefaultClient(context.Background(), vertexScope)
	if err != nil {
		return nil, fmt.Errorf("google.DefaultClient: %w", err)
	}
	c.Timeout = 45 * time.Second
	t.client = c
	return c, nil
}

const translatePrompt = "You are a professional Japanese-to-English translator for an autonomous-vehicle test engineering team. " +
	"Translate the message below into natural, precise English. Preserve technical terms, identifiers, URLs, numbers and line breaks. " +
	"Output only the translation, with no preamble or commentary.\n\n"

// toEnglish translates text (typically Japanese) into English.
func (t *translator) toEnglish(ctx context.Context, text string) (string, error) {
	return t.generate(ctx, translatePrompt+text)
}

const diffSummaryPrompt = "You explain software changes to vehicle test operators who are not software engineers. " +
	"Below are the changes between last night's build and tonight's build of the self-driving truck software, grouped by area. " +
	"Write at most 6 short plain-English bullet points (start each with '- '), one per area that changed, in this order: HMI (what the driver sees and hears), Behavior (driving decisions), Planner (path and speed), Prediction (other road users), bug fixes. " +
	"Say what a driver or tester might notice. Avoid jargon, code names, file paths, ticket numbers and PR numbers. Do not invent details; if a change is unclear, say 'minor internal change'. No headings, no preamble.\n\n"

// summarizeDiff produces a short narrative of a categorized nightly diff.
func (t *translator) summarizeDiff(ctx context.Context, d *diffSummary) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Compare: %s -> %s (%d changes, %d of them outside the areas below)\n\n", d.Base, d.Head, d.TotalCommits, d.OtherCount)
	for _, c := range d.Categories {
		if len(c.Items) == 0 && c.Undescribed == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n", c.Label)
		for i, it := range c.Items {
			if i == 40 {
				fmt.Fprintf(&b, "- (+%d more)\n", len(c.Items)-40)
				break
			}
			fmt.Fprintf(&b, "- %s", it.Headline)
			if it.Summary != "" {
				fmt.Fprintf(&b, " — %s", it.Summary)
			}
			b.WriteString("\n")
		}
		if c.Undescribed > 0 {
			fmt.Fprintf(&b, "- (%d automated or undescribed changes also touched this area)\n", c.Undescribed)
		}
		b.WriteString("\n")
	}
	return t.generate(ctx, diffSummaryPrompt+b.String())
}

// generate sends a single-turn prompt to Gemini on Vertex AI and returns the
// text of the first candidate.
func (t *translator) generate(ctx context.Context, prompt string) (string, error) {
	if t.disabled {
		return "", fmt.Errorf("translation disabled")
	}
	client, err := t.httpClient()
	if err != nil {
		return "", err
	}

	u := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		t.location, t.project, t.location, t.model)
	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{{
			"role":  "user",
			"parts": []map[string]string{{"text": prompt}},
		}},
		"generationConfig": map[string]interface{}{"temperature": 0},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		return "", fmt.Errorf("vertex ai %s: %s", resp.Status, truncate(apiErr.Error.Message, 200))
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 {
		return "", fmt.Errorf("vertex ai returned no candidates")
	}
	var b strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		b.WriteString(p.Text)
	}
	result := strings.TrimSpace(b.String())
	if result == "" {
		return "", fmt.Errorf("vertex ai returned an empty translation")
	}
	return result, nil
}
