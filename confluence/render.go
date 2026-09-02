package confluence

import (
	"fmt"
	"html"
	"strings"
)

// CheckResult is one line item in a checklist table: e.g. "run_syscheck results".
type CheckResult struct {
	Key    string // form field key, e.g. "syscheck"
	Label  string // human label, e.g. "run_syscheck results"
	Result string // "pass" | "fail" | "na"
	Notes  string
}

// DiffItem is one described on-truck commit in the nightly diff summary.
type DiffItem struct {
	Title      string // raw commit title
	Headline   string // human-readable title (no ticket/PR/bracket noise)
	PlainTitle string // headline with jargon swapped for plain words
	URL        string
	Summary    string
	Excerpt    string // first sentences of the PR description
	Note       string // "what this means for the truck" (behavior-affecting changes)
	Kind       string
	Jira       string
	PR         int
	PRURL      string
	Tags       []string
	Impact     string // visible | driving | internal | ""
	NeedsInfo  bool   // affects the truck but has no usable description
	IsFix      bool
	IsRevert   bool
	SHA        string
	Origin     string // source-repo revision of an automatic sync
	Dirs       []string
	Files      []string
}

// DiffCategory groups described on-truck commits under an area (HMI,
// Behavior, …); Undescribed counts automated/housekeeping commits that also
// touched it (UndescribedDriving: those touching driving/driver-facing code)
// and Impact tallies every on-truck commit in the area by where it runs.
type DiffCategory struct {
	Key                string
	Label              string
	Items              []DiffItem
	Undescribed        int
	UndescribedDriving int
	Flagged            []DiffItem // undescribed commits that touched driving/driver-facing code
	Impact             map[string]int
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// impactLabels are the reader-facing tags for where a change runs.
var impactLabels = map[string]string{
	"visible":  "You'll see it on the truck",
	"driving":  "Changes how the truck drives — you might notice",
	"internal": "On the truck, but hard to spot",
}

// impactSentence summarises an area's on-truck changes by where they run, e.g.
// "1 affects how the truck drives; 2 run on the truck but are hard to spot."
func impactSentence(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	var parts []string
	if n := counts["visible"]; n > 0 {
		parts = append(parts, plural(n, "is easy to spot on the truck", "are easy to spot on the truck"))
	}
	if n := counts["driving"]; n > 0 {
		parts = append(parts, plural(n, "affects how the truck drives", "affect how the truck drives"))
	}
	if n := counts["internal"]; n > 0 {
		parts = append(parts, plural(n, "runs on the truck but is hard to spot", "run on the truck but are hard to spot"))
	}
	if n := counts["unknown"]; n > 0 {
		parts = append(parts, plural(n, "could not be checked", "could not be checked"))
	}
	if len(parts) == 0 {
		return ""
	}
	s := strings.Join(parts, "; ")
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

func affectsBehavior(impact string) bool { return impact == "visible" || impact == "driving" }

// bestLink prefers the pull request page over the raw commit page.
func bestLink(it DiffItem) string {
	if it.PRURL != "" {
		return it.PRURL
	}
	return it.URL
}

// linkLabel is the short text for the GitHub link ("PR #113119" or "commit").
func linkLabel(it DiffItem) string {
	if it.PR > 0 {
		return fmt.Sprintf("PR #%d", it.PR)
	}
	return "commit"
}

// SimpleArea / SimpleLang are the model-written plain-language rendering of
// one area / one language (optional; templates are used when absent).
type SimpleArea struct {
	Sentence string
	Bullets  []string
}

type SimpleLang struct {
	Overall string
	Areas   map[string]SimpleArea
}

// DiffSummary is the build-over-build change summary shown at the top of the page.
type DiffSummary struct {
	Repo           string
	Base           string
	Head           string
	BaseDate       string
	HeadDate       string
	CompareURL     string
	TotalCommits   int
	Categories     []DiffCategory
	OtherCount     int // on-truck commits outside every category
	OtherAutomated int // of which automated system (Vehicle OS) syncs
	Impact         map[string]int
	Ignored        int // tools/simulation/test commits, not shown
	OnTruck        int
	Simple         map[string]SimpleLang
	AISummary      string
	Notes          string // tester's free-text notes
}

// categoryExplanations are the plain-language descriptions shown next to each area.
var categoryExplanations = map[string]string{
	"hmi":        "what the driver sees and hears",
	"behavior":   "driving decisions such as yielding, lane changes and stops",
	"planner":    "path and speed planning",
	"prediction": "predicting what other road users will do",
	"fixes":      "bug fixes and reverted changes",
}

var categoryEmoji = map[string]string{
	"hmi": "🖥️", "behavior": "🚦", "planner": "🗺️", "prediction": "🔮", "fixes": "🛠️",
}

// simpleSentence is the template ("explain it to a 10-year-old") used when the
// model has not written one.
func simpleSentence(key string, n int) string {
	changes := plural(n, "change", "changes")
	switch key {
	case "hmi":
		if n == 0 {
			return "Nothing changed on the driver's screen or in the sounds it makes."
		}
		return changes + " to what the driver sees and hears on the screen."
	case "behavior":
		if n == 0 {
			return "The truck makes the same driving decisions as before."
		}
		return changes + " to how the truck decides what to do — like when to slow down, stop or change lanes."
	case "planner":
		if n == 0 {
			return "No changes to how the truck plans where to drive and how fast."
		}
		return changes + " to how the truck plans where to drive and how fast."
	case "prediction":
		if n == 0 {
			return "No changes to how the truck guesses what other cars and people will do."
		}
		return changes + " to how the truck guesses what other cars and people will do next."
	case "fixes":
		if n == 0 {
			return "No bug fixes on the truck tonight."
		}
		return plural(n, "fix", "fixes") + ": problems that were found and repaired, or changes that were undone."
	}
	return changes + "."
}

// RunReport holds everything submitted from the form for one smoke test run.
type RunReport struct {
	Tag           string
	Date          string // YYYY-MM-DD
	Vehicle       string
	TestEngineer  string
	CommitHash    string
	SlackThread   string
	RecordingLink string
	OverallResult string // "pass" | "fail" | "partial"
	RunID         string

	Diff *DiffSummary // nil when the diff was not loaded

	Preflight     []CheckResult
	Engagement    []CheckResult
	Disengagement []CheckResult

	DisengagementRunID           string
	DisengagementClosedLoopRunID string
}

func esc(s string) string {
	return html.EscapeString(s)
}

func resultBadge(result string) string {
	switch strings.ToLower(result) {
	case "pass":
		return `<ac:structured-macro ac:name="status"><ac:parameter ac:name="colour">Green</ac:parameter><ac:parameter ac:name="title">PASS</ac:parameter></ac:structured-macro>`
	case "fail":
		return `<ac:structured-macro ac:name="status"><ac:parameter ac:name="colour">Red</ac:parameter><ac:parameter ac:name="title">FAIL</ac:parameter></ac:structured-macro>`
	default:
		return `<ac:structured-macro ac:name="status"><ac:parameter ac:name="colour">Grey</ac:parameter><ac:parameter ac:name="title">N/A</ac:parameter></ac:structured-macro>`
	}
}

func checksTable(title string, items []CheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<h2>%s</h2>\n", esc(title))
	b.WriteString("<table><thead><tr><th>Check</th><th>Result</th><th>Notes</th></tr></thead><tbody>\n")
	for _, item := range items {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			esc(item.Label), resultBadge(item.Result), esc(item.Notes))
	}
	b.WriteString("</tbody></table>\n")
	return b.String()
}

func nl2br(s string) string {
	return strings.ReplaceAll(esc(s), "\n", "<br/>")
}

// friendlyDate turns a tag stamp "2026-09-01" or "2026-08-26-01" into "Sep 1"
// ("Aug 26 #01" for numbered builds); falls back to the input.
func friendlyDate(stamp string) string {
	if len(stamp) < 10 {
		return stamp
	}
	var y, m, d int
	if _, err := fmt.Sscanf(stamp[:10], "%d-%d-%d", &y, &m, &d); err != nil || m < 1 || m > 12 {
		return stamp
	}
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	s := fmt.Sprintf("%s %d", months[m-1], d)
	if len(stamp) > 11 {
		s += " #" + stamp[11:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// renderDiff writes the "What changed since the previous build" section: the
// number of changes, one plain-language sentence (and simple bullets) per
// area, counts for everything else, and the engineering detail folded into an
// expand macro.
func renderDiff(b *strings.Builder, d *DiffSummary) {
	b.WriteString("<h2>What changed since the previous build</h2>\n")
	from, to := friendlyDate(d.BaseDate), friendlyDate(d.HeadDate)
	if from == "" || to == "" {
		from, to = d.Base, d.Head
	}
	onTruck := d.OnTruck
	if onTruck == 0 && d.Ignored == 0 {
		onTruck = d.TotalCommits
	}
	fmt.Fprintf(b, "<p><strong>%s on the truck since the previous build</strong> (%s → %s)", esc(plural(onTruck, "change", "changes")), esc(from), esc(to))
	if len(d.Impact) > 0 {
		notice := d.Impact["visible"] + d.Impact["driving"]
		fmt.Fprintf(b, " — <strong>%s</strong> (%d on the driver's screen, %d in how it drives)",
			esc(plural(notice, "you might notice", "you might notice")), d.Impact["visible"], d.Impact["driving"])
	}
	if d.CompareURL != "" {
		fmt.Fprintf(b, " · <a href=\"%s\">Full list on GitHub</a>", esc(d.CompareURL))
	}
	fmt.Fprintf(b, "<br/><span style=\"color:#6b778c\">Builds: %s → %s", esc(d.Base), esc(d.Head))
	if d.Ignored > 0 {
		fmt.Fprintf(b, " · %s not shown (tools, simulation, tests)", esc(plural(d.Ignored, "change", "changes")))
	}
	b.WriteString("</span></p>\n")

	var simple *SimpleLang
	if s, ok := d.Simple["en"]; ok {
		simple = &s
	}
	overall := d.AISummary
	if simple != nil && simple.Overall != "" {
		overall = simple.Overall
	}
	if overall != "" {
		b.WriteString("<p><strong>In short:</strong> ")
		b.WriteString(nl2br(overall))
		b.WriteString("</p>\n")
	}
	if d.Notes != "" {
		b.WriteString("<p><strong>Tester notes:</strong> ")
		b.WriteString(nl2br(d.Notes))
		b.WriteString("</p>\n")
	}

	for _, c := range d.Categories {
		total := len(c.Items) + c.Undescribed
		fmt.Fprintf(b, "<h3>%s %s (%d)</h3>\n", categoryEmoji[c.Key], esc(c.Label), total)
		sentence := simpleSentence(c.Key, total)
		var bullets []string
		if simple != nil {
			if a, ok := simple.Areas[c.Key]; ok {
				if a.Sentence != "" {
					sentence = a.Sentence
				}
				bullets = a.Bullets
			}
		}
		if total > 0 && (simple == nil || simple.Areas[c.Key].Sentence == "") {
			if is := impactSentence(c.Impact); is != "" {
				sentence += " " + is
			}
		}
		fmt.Fprintf(b, "<p>%s</p>\n", esc(sentence))

		if bullets != nil {
			// Model-written bullets (already plain language, one per described item).
			if len(bullets) > 0 {
				b.WriteString("<ul>\n")
				for i, l := range bullets {
					fmt.Fprintf(b, "<li>%s", esc(l))
					if i < len(c.Items) {
						if link := bestLink(c.Items[i]); link != "" {
							fmt.Fprintf(b, " <a href=\"%s\">%s</a>", esc(link), esc(linkLabel(c.Items[i])))
						}
						if c.Items[i].NeedsInfo {
							b.WriteString(" <strong>⚠️ no description — ask the author before testing</strong>")
						}
					}
					b.WriteString("</li>\n")
				}
				b.WriteString("</ul>\n")
			}
		} else {
			// Template rendering: behavior-affecting changes get a card with a
			// plain title, a "what this means" note and the description excerpt.
			for _, it := range c.Items {
				if !affectsBehavior(it.Impact) {
					continue
				}
				title := it.PlainTitle
				if title == "" {
					title = it.Headline
				}
				tag := "🚚 Changes how the truck drives"
				if it.Impact == "visible" {
					tag = "👀 You'll see it on the driver's screen"
				}
				b.WriteString(`<ac:structured-macro ac:name="panel"><ac:parameter ac:name="bgColor">#EAE6FF</ac:parameter><ac:rich-text-body>` + "\n")
				fmt.Fprintf(b, "<p><strong>%s</strong><br/>", esc(tag))
				if link := bestLink(it); link != "" {
					fmt.Fprintf(b, "<a href=\"%s\">%s</a>", esc(link), esc(title))
				} else {
					b.WriteString(esc(title))
				}
				if it.IsRevert {
					b.WriteString(" (reverted change)")
				}
				if it.PRURL != "" {
					fmt.Fprintf(b, " — <a href=\"%s\">%s on GitHub</a>", esc(it.PRURL), esc(linkLabel(it)))
					if it.URL != "" {
						fmt.Fprintf(b, " · <a href=\"%s\">commit</a>", esc(it.URL))
					}
				} else if it.URL != "" {
					fmt.Fprintf(b, " — <a href=\"%s\">commit on GitHub</a>", esc(it.URL))
				}
				if it.Headline != "" && it.Headline != title {
					fmt.Fprintf(b, "<br/><span style=\"color:#6b778c\">Engineer's title: %s</span>", esc(it.Headline))
				}
				b.WriteString("</p>\n")
				if it.Note != "" {
					fmt.Fprintf(b, "<p><em>What this means:</em> %s</p>\n", esc(it.Note))
				}
				if it.Excerpt != "" {
					fmt.Fprintf(b, "<p><em>The engineer wrote:</em> %s</p>\n", esc(it.Excerpt))
				}
				if it.NeedsInfo {
					b.WriteString("<p><strong>⚠️ No description — ask the author what this changes before testing.</strong></p>\n")
				}
				b.WriteString("</ac:rich-text-body></ac:structured-macro>\n")
			}
			var rest []DiffItem
			for _, it := range c.Items {
				if !affectsBehavior(it.Impact) {
					rest = append(rest, it)
				}
			}
			if len(rest) > 0 {
				b.WriteString("<ul>\n")
				for _, it := range rest {
					title := it.PlainTitle
					if title == "" {
						title = it.Headline
					}
					if link := bestLink(it); link != "" {
						fmt.Fprintf(b, "<li><a href=\"%s\">%s</a> <span style=\"color:#6b778c\">— on the truck, but hard to spot · %s</span></li>\n", esc(link), esc(title), esc(linkLabel(it)))
					} else {
						fmt.Fprintf(b, "<li>%s <span style=\"color:#6b778c\">— on the truck, but hard to spot</span></li>\n", esc(title))
					}
				}
				b.WriteString("</ul>\n")
			}
		}
		if c.UndescribedDriving > 0 {
			fmt.Fprintf(b, "<p><strong>⚠️ %s driving or driver-facing code with no description</strong> (automatic updates). Worth asking about before testing.</p>\n",
				esc(plural(c.UndescribedDriving, "automatic update touched", "automatic updates touched")))
			if len(c.Flagged) > 0 {
				b.WriteString("<ul>\n")
				for _, f := range c.Flagged {
					b.WriteString("<li>")
					if f.URL != "" {
						fmt.Fprintf(b, "<a href=\"%s\">Automatic sync %s</a>", esc(f.URL), esc(shortSHA(f.SHA)))
					} else {
						fmt.Fprintf(b, "Automatic sync %s", esc(shortSHA(f.SHA)))
					}
					b.WriteString(" — no pull request (copied in from the main repository")
					if f.Origin != "" {
						fmt.Fprintf(b, ", source revision %s", esc(shortSHA(f.Origin)))
					}
					b.WriteString(")")
					if len(f.Files) > 0 {
						fmt.Fprintf(b, "<br/><span style=\"color:#6b778c\">Files: %s</span>", esc(strings.Join(f.Files, ", ")))
					} else if len(f.Dirs) > 0 {
						fmt.Fprintf(b, "<br/><span style=\"color:#6b778c\">Areas: %s</span>", esc(strings.Join(f.Dirs, ", ")))
					}
					b.WriteString("</li>\n")
				}
				b.WriteString("</ul>\n")
			}
		}
		if rest := c.Undescribed - c.UndescribedDriving; rest > 0 && total > 0 {
			if rest == 1 {
				b.WriteString("<p><span style=\"color:#6b778c\">1 more is a small automatic update without a description (hard to spot).</span></p>\n")
			} else {
				fmt.Fprintf(b, "<p><span style=\"color:#6b778c\">%d more are small automatic updates without a description (hard to spot).</span></p>\n", rest)
			}
		}
	}

	if d.OtherCount > 0 {
		fmt.Fprintf(b, "<p><strong>Also:</strong> %s on the truck that don't affect these areas", esc(plural(d.OtherCount, "other change", "other changes")))
		if d.OtherAutomated == 1 {
			b.WriteString(" (1 of them is an automatic system update)")
		} else if d.OtherAutomated > 1 {
			fmt.Fprintf(b, " (%d of them are automatic system updates)", d.OtherAutomated)
		}
		b.WriteString(".</p>\n")
	}

	// Engineering detail, folded away.
	b.WriteString(`<ac:structured-macro ac:name="expand"><ac:parameter ac:name="title">Technical details</ac:parameter><ac:rich-text-body>` + "\n")
	renderTechnical(b, d)
	b.WriteString("</ac:rich-text-body></ac:structured-macro>\n")
}

// renderTechnical lists the described commits per area with links and metadata.
func renderTechnical(b *strings.Builder, d *DiffSummary) {
	for _, c := range d.Categories {
		total := len(c.Items) + c.Undescribed
		fmt.Fprintf(b, "<h4>%s (%d)</h4>\n", esc(c.Label), total)
		if expl := categoryExplanations[c.Key]; expl != "" {
			fmt.Fprintf(b, "<p><span style=\"color:#6b778c\">%s</span></p>\n", esc(expl))
		}
		if total == 0 {
			b.WriteString("<p><em>No changes</em></p>\n")
			continue
		}
		if len(c.Items) > 0 {
			b.WriteString("<ul>\n")
			for _, it := range c.Items {
				headline := it.Headline
				if headline == "" {
					headline = it.Title
				}
				b.WriteString("<li>")
				if link := bestLink(it); link != "" {
					fmt.Fprintf(b, "<a href=\"%s\">%s</a>", esc(link), esc(headline))
				} else {
					b.WriteString(esc(headline))
				}
				if it.IsRevert {
					b.WriteString(" <strong>(reverted change)</strong>")
				} else if it.IsFix && c.Key != "fixes" {
					b.WriteString(" <strong>(fix)</strong>")
				}
				if lbl := impactLabels[it.Impact]; lbl != "" {
					fmt.Fprintf(b, " <span style=\"color:#6b778c\">[%s]</span>", esc(lbl))
				}
				if it.Summary != "" {
					fmt.Fprintf(b, "<br/><em>%s</em>", esc(it.Summary))
				}
				var meta []string
				if it.PR > 0 {
					meta = append(meta, fmt.Sprintf("PR #%d", it.PR))
				}
				if it.Jira != "" {
					meta = append(meta, it.Jira)
				}
				if len(it.Tags) > 0 {
					meta = append(meta, strings.Join(it.Tags, ", "))
				}
				if len(meta) > 0 {
					fmt.Fprintf(b, "<br/><span style=\"color:#6b778c;font-size:smaller\">%s</span>", esc(strings.Join(meta, " · ")))
				}
				b.WriteString("</li>\n")
			}
			b.WriteString("</ul>\n")
		}
		if c.Undescribed > 0 {
			fmt.Fprintf(b, "<p><span style=\"color:#6b778c\">%s without a description also touched this area (automated updates).</span></p>\n",
				esc(plural(c.Undescribed, "more change", "more changes")))
		}
	}

	if d.OtherCount > 0 {
		fmt.Fprintf(b, "<p><strong>Other changes:</strong> %s outside the areas above", esc(plural(d.OtherCount, "change", "changes")))
		if d.OtherAutomated > 0 {
			fmt.Fprintf(b, " (%d automated system updates)", d.OtherAutomated)
		}
		if d.CompareURL != "" {
			fmt.Fprintf(b, " — <a href=\"%s\">see the full list on GitHub</a>", esc(d.CompareURL))
		}
		b.WriteString(".</p>\n")
	}
}

// RenderStorageFormat builds the Confluence "storage format" XHTML body for a run report.
func RenderStorageFormat(r RunReport) string {
	var b strings.Builder

	if r.Diff != nil {
		renderDiff(&b, r.Diff)
	}

	b.WriteString("<h2>Run Summary</h2>\n")
	b.WriteString("<table><tbody>\n")
	rows := [][2]string{
		{"Tag", r.Tag},
		{"Date", r.Date},
		{"Vehicle", r.Vehicle},
		{"Test Engineer", r.TestEngineer},
		{"Commit Hash", r.CommitHash},
		{"Run ID", r.RunID},
	}
	for _, row := range rows {
		fmt.Fprintf(&b, "<tr><th>%s</th><td>%s</td></tr>\n", esc(row[0]), esc(row[1]))
	}
	if r.SlackThread != "" {
		fmt.Fprintf(&b, "<tr><th>Slack Thread</th><td><a href=\"%s\">%s</a></td></tr>\n", esc(r.SlackThread), esc(r.SlackThread))
	} else {
		b.WriteString("<tr><th>Slack Thread</th><td></td></tr>\n")
	}
	if r.RecordingLink != "" {
		fmt.Fprintf(&b, "<tr><th>Recording</th><td><a href=\"%s\">%s</a></td></tr>\n", esc(r.RecordingLink), esc(r.RecordingLink))
	} else {
		b.WriteString("<tr><th>Recording</th><td></td></tr>\n")
	}
	fmt.Fprintf(&b, "<tr><th>Overall Result</th><td>%s</td></tr>\n", resultBadge(r.OverallResult))
	b.WriteString("</tbody></table>\n")

	b.WriteString(checksTable("Preflight Checks", r.Preflight))
	b.WriteString(checksTable("Engagement Checks", r.Engagement))

	b.WriteString("<h2>Disengagement Checks</h2>\n")
	b.WriteString("<table><tbody>\n")
	fmt.Fprintf(&b, "<tr><th>Run ID</th><td>%s</td></tr>\n", esc(r.DisengagementRunID))
	fmt.Fprintf(&b, "<tr><th>Closed Loop Run ID</th><td>%s</td></tr>\n", esc(r.DisengagementClosedLoopRunID))
	b.WriteString("</tbody></table>\n")
	b.WriteString("<table><thead><tr><th>Check</th><th>Result</th><th>Notes</th></tr></thead><tbody>\n")
	for _, item := range r.Disengagement {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			esc(item.Label), resultBadge(item.Result), esc(item.Notes))
	}
	b.WriteString("</tbody></table>\n")

	return b.String()
}

// PageTitle builds the title for the run's Confluence page.
func PageTitle(r RunReport) string {
	return fmt.Sprintf("%s — %s — Run %s", r.Tag, r.Date, r.RunID)
}
