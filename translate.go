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

const simplifyPrompt = `You explain software changes to a 10-year-old. Below are tonight's changes to a self-driving truck's software, grouped by area:
- hmi: the screen and sounds the driver sees and hears
- behavior: how the truck decides what to do (slow down, stop, change lanes, let others pass)
- planner: how the truck plans where to drive and how fast
- prediction: how the truck guesses what other cars and people will do next
- fixes: problems that were found and repaired, or changes that were undone

Return ONLY JSON, no markdown, with exactly this shape:
{"en":{"overall":"one or two short friendly sentences about tonight's build",
       "areas":{"hmi":{"sentence":"one simple sentence saying how many things changed here and what they are about","bullets":["one very simple line per change, under 12 words"]},
                "behavior":{"sentence":"...","bullets":[]},"planner":{"sentence":"...","bullets":[]},"prediction":{"sentence":"...","bullets":[]},"fixes":{"sentence":"...","bullets":[]}}},
 "ja":{ the same structure written in simple, friendly Japanese }}

Each change is tagged with where it runs (changes that are NOT on the truck have already been removed):
- [visible] = on the truck and easy to spot (driver screen, sounds, start-up, health monitor)
- [driving] = on the truck and changes how it drives — a tester might notice while driving
- [internal] = on the truck but hard to spot (sensing, positioning, models, drivers, vehicle OS)
Each change also has a kind (fix, revert, speedup, tuning, refactor, new, removal) and, when available, an excerpt of the engineer's description.

Take extra care with [visible] and [driving] changes: in TWO short sentences explain what the truck (or the driver's screen) will do differently and in which situation a tester could notice it, based only on the excerpt. Start those bullets with "You'll see it: " or "You might notice: ". For [internal] changes use one line starting with "Behind the scenes: ".
If a [visible] or [driving] change has no excerpt, write exactly: "You might notice: (no description — ask the author before testing) " followed by the plain title.

Rules: use very simple everyday words a 10-year-old knows; no jargon, code names, file paths, ticket numbers or PR numbers. If an area has no changes, say so in one friendly sentence and give an empty bullets list. Never invent details. Mention counts of automatic updates only as "small automatic updates".

`

// simplifyDiff asks the model for the plain-language rendering (English and
// Japanese) of a categorized diff.
func (t *translator) simplifyDiff(ctx context.Context, d *diffSummary) (map[string]simpleLang, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Builds: %s -> %s. %d changes run on the truck (%d tools/simulation/test changes were removed). %d on-truck changes are outside the areas below (%d are automatic system updates).\n\n",
		d.Base, d.Head, d.OnTruck, d.Ignored, d.OtherCount, d.OtherAutomated)
	for _, c := range d.Categories {
		fmt.Fprintf(&b, "## %s (%d changes; %d automatic/undescribed, of which %d touched driving or driver-facing code; %d visible, %d driving, %d internal)\n",
			c.Key, len(c.Items)+c.Undescribed, c.Undescribed, c.UndescribedDriving, c.Impact[impactVisible], c.Impact[impactDriving], c.Impact[impactInternal])
		for i, it := range c.Items {
			if i == 40 {
				fmt.Fprintf(&b, "- (+%d more)\n", len(c.Items)-40)
				break
			}
			fmt.Fprintf(&b, "- [%s][%s] %s", impactKey(it.Impact), it.Kind, it.Headline)
			if it.PlainTitle != "" && it.PlainTitle != it.Headline {
				fmt.Fprintf(&b, " (plain: %s)", it.PlainTitle)
			}
			if it.Excerpt != "" {
				fmt.Fprintf(&b, "\n  excerpt: %s", it.Excerpt)
			} else if affectsTruckBehavior(it.Impact) {
				b.WriteString("\n  excerpt: (none)")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	raw, err := t.generateJSON(ctx, simplifyPrompt+b.String())
	if err != nil {
		return nil, err
	}
	return parseSimpleJSON(raw)
}

// parseSimpleJSON decodes the model's JSON (tolerating a ```json fence) and
// checks that at least the English rendering is present.
func parseSimpleJSON(raw string) (map[string]simpleLang, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	var out map[string]simpleLang
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &out); err != nil {
		return nil, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	en, ok := out["en"]
	if !ok || len(en.Areas) == 0 {
		return nil, fmt.Errorf("model JSON has no English areas")
	}
	return out, nil
}

// generate sends a single-turn prompt to Gemini on Vertex AI and returns the
// text of the first candidate.
func (t *translator) generate(ctx context.Context, prompt string) (string, error) {
	return t.generateWith(ctx, prompt, false)
}

// generateJSON is generate with the model constrained to emit JSON.
func (t *translator) generateJSON(ctx context.Context, prompt string) (string, error) {
	return t.generateWith(ctx, prompt, true)
}

func (t *translator) generateWith(ctx context.Context, prompt string, jsonMode bool) (string, error) {
	if t.disabled {
		return "", fmt.Errorf("translation disabled")
	}
	client, err := t.httpClient()
	if err != nil {
		return "", err
	}

	u := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		t.location, t.project, t.location, t.model)
	genCfg := map[string]interface{}{"temperature": 0}
	if jsonMode {
		genCfg["responseMimeType"] = "application/json"
	}
	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{{
			"role":  "user",
			"parts": []map[string]string{{"text": prompt}},
		}},
		"generationConfig": genCfg,
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
