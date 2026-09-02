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

// DiffItem is one described commit in the nightly diff summary.
type DiffItem struct {
	Title    string // raw commit title
	Headline string // human-readable title (no ticket/PR/bracket noise)
	URL      string
	Summary  string
	Jira     string
	PR       int
	Tags     []string
	IsFix    bool
	IsRevert bool
}

// DiffCategory groups described commits under an area (HMI, Behavior, …);
// Undescribed counts automated/housekeeping commits that also touched it.
type DiffCategory struct {
	Key         string
	Label       string
	Items       []DiffItem
	Undescribed int
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
	OtherCount     int // commits outside every category
	OtherAutomated int // of which automated system (Vehicle OS) syncs
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

// renderDiff writes the "What changed since the previous build" section in
// plain language: described changes per area, counts for everything else.
func renderDiff(b *strings.Builder, d *DiffSummary) {
	b.WriteString("<h2>What changed since the previous build</h2>\n")
	from, to := friendlyDate(d.BaseDate), friendlyDate(d.HeadDate)
	if from == "" || to == "" {
		from, to = d.Base, d.Head
	}
	fmt.Fprintf(b, "<p><strong>%s → %s</strong>: %s in total", esc(from), esc(to), plural(d.TotalCommits, "change", "changes"))
	var parts []string
	for _, c := range d.Categories {
		parts = append(parts, fmt.Sprintf("%s %d", esc(c.Label), len(c.Items)+c.Undescribed))
	}
	if len(parts) > 0 {
		fmt.Fprintf(b, " — %s", strings.Join(parts, " · "))
	}
	b.WriteString(".")
	if d.CompareURL != "" {
		fmt.Fprintf(b, " <a href=\"%s\">Full list on GitHub</a>", esc(d.CompareURL))
	}
	fmt.Fprintf(b, "<br/><span style=\"color:#6b778c\">Builds: %s → %s</span></p>\n", esc(d.Base), esc(d.Head))

	if d.AISummary != "" {
		b.WriteString("<h3>In short</h3>\n<p>")
		b.WriteString(nl2br(d.AISummary))
		b.WriteString("</p>\n")
	}
	if d.Notes != "" {
		b.WriteString("<h3>Tester notes</h3>\n<p>")
		b.WriteString(nl2br(d.Notes))
		b.WriteString("</p>\n")
	}

	for _, c := range d.Categories {
		total := len(c.Items) + c.Undescribed
		fmt.Fprintf(b, "<h3>%s (%d)</h3>\n", esc(c.Label), total)
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
				if it.URL != "" {
					fmt.Fprintf(b, "<a href=\"%s\">%s</a>", esc(it.URL), esc(headline))
				} else {
					b.WriteString(esc(headline))
				}
				if it.IsRevert {
					b.WriteString(" <strong>(reverted change)</strong>")
				} else if it.IsFix && c.Key != "fixes" {
					b.WriteString(" <strong>(fix)</strong>")
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
