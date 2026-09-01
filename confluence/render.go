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

// RunReport holds everything submitted from the form for one smoke test run.
type RunReport struct {
	Tag           string
	Date          string // YYYY-MM-DD
	Vehicle       string
	TestEngineer  string
	CommitHash    string
	SlackThread   string
	OverallResult string // "pass" | "fail" | "partial"
	RunID         string

	Preflight     []CheckResult
	Engagement    []CheckResult
	Disengagement []CheckResult
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

// RenderStorageFormat builds the Confluence "storage format" XHTML body for a run report.
func RenderStorageFormat(r RunReport) string {
	var b strings.Builder

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
	fmt.Fprintf(&b, "<tr><th>Overall Result</th><td>%s</td></tr>\n", resultBadge(r.OverallResult))
	b.WriteString("</tbody></table>\n")

	b.WriteString(checksTable("Preflight Checks", r.Preflight))
	b.WriteString(checksTable("Engagement Checks", r.Engagement))
	b.WriteString(checksTable("Disengagement Checks", r.Disengagement))

	return b.String()
}

// PageTitle builds the title for the run's Confluence page.
func PageTitle(r RunReport) string {
	return fmt.Sprintf("%s — %s — Run %s", r.Tag, r.Date, r.RunID)
}
