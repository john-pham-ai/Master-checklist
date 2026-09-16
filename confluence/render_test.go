package confluence

import (
	"strings"
	"testing"
)

func TestSlackToHTMLEscapes(t *testing.T) {
	// Everything except the recognized mrkdwn constructs must be escaped.
	got := slackToHTML(`<script>alert("x")</script> & <b>not html</b>`)
	want := "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; &lt;b&gt;not html&lt;/b&gt;"
	if got != want {
		t.Errorf("slackToHTML = %q; want %q", got, want)
	}
}

func TestSlackToHTMLLinks(t *testing.T) {
	// Labelled mrkdwn link, bare autolink, and an empty label falling back
	// to the URL itself.
	got := slackToHTML("see <https://example.com/a?b=1|the example> and <https://example.com/b>")
	want := `see <a href="https://example.com/a?b=1">the example</a> and <a href="https://example.com/b">https://example.com/b</a>`
	if got != want {
		t.Errorf("slackToHTML = %q; want %q", got, want)
	}
}

func TestSlackToHTMLBoldAndLineBreaks(t *testing.T) {
	// A realistic GO decision message: bold markers, mrkdwn links and
	// newlines all appear.
	in := "Recommended decision: *GO* :large_green_circle:\n" +
		"• 1 regression and 2 progressions.\n" +
		"Open issues: <https://appliedint-frontier.atlassian.net/browse/ROOT-5146|[Sim Triage][Master][SDS] Map Lane>"
	got := slackToHTML(in)
	for _, want := range []string{
		"<strong>GO</strong>",
		":large_green_circle:<br/>\n• 1 regression",
		`<a href="https://appliedint-frontier.atlassian.net/browse/ROOT-5146">[Sim Triage][Master][SDS] Map Lane</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("slackToHTML = %q; want to contain %q", got, want)
		}
	}
	// The raw < and > of the mrkdwn link must not survive unescaped.
	if strings.Contains(got, "<https://") {
		t.Errorf("slackToHTML = %q; mrkdwn link not converted", got)
	}
}

func TestSlackToHTMLEmptyAndOddInput(t *testing.T) {
	if got := slackToHTML(""); got != "" {
		t.Errorf("slackToHTML(\"\") = %q", got)
	}
	// Unbalanced asterisks stay literal rather than mangling the message.
	if got := slackToHTML("*a or b"); !strings.Contains(got, "*a or b") {
		t.Errorf("slackToHTML unbalanced bold = %q", got)
	}
}

func TestRenderClosedLoopApproval(t *testing.T) {
	r := RunReport{
		Tag: "trucking-scheduled-night-2026-09-15", Date: "2026-09-16", Vehicle: "810",
		TestEngineer: "John Pham", CommitHash: "abc123", RunID: "R42",
		ClosedLoop: ClosedLoop{
			Enabled:         true,
			RunID:           "CL99",
			ApprovalLink:    "https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789488051626809?thread_ts=1789474157.142449",
			ApprovalMessage: "Recommended decision: *GO* :large_green_circle:",
		},
	}
	got := RenderStorageFormat(r)
	for _, want := range []string{
		`<tr><th>GO Approval (Slack)</th><td><a href="https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789488051626809?thread_ts=1789474157.142449">https://grid-appliedint.enterprise.slack.com/archives/C0B1L8F6NUX/p1789488051626809?thread_ts=1789474157.142449</a></td></tr>`,
		"<strong>GO</strong>",
		"GO approval message",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

func TestRenderClosedLoopWithoutApproval(t *testing.T) {
	// The approval rows/message are optional (and master-only): a closed
	// loop run without them renders no GO approval output at all.
	r := RunReport{
		Tag: "t", Date: "2026-09-16", Vehicle: "810", TestEngineer: "e", CommitHash: "c", RunID: "r",
		ClosedLoop: ClosedLoop{Enabled: true, RunID: "CL99"},
	}
	got := RenderStorageFormat(r)
	if strings.Contains(got, "GO Approval") || strings.Contains(got, "GO approval message") {
		t.Errorf("rendered page has GO approval output without approval data:\n%s", got)
	}
}

func TestRenderClosedLoopDisabled(t *testing.T) {
	// No closed loop toggle: no closed loop output at all.
	r := RunReport{Tag: "t", Date: "2026-09-16", Vehicle: "810", TestEngineer: "e", CommitHash: "c", RunID: "r"}
	got := RenderStorageFormat(r)
	if strings.Contains(got, "Closed Loop") || strings.Contains(got, "GO Approval") {
		t.Errorf("rendered page has closed loop output while disabled")
	}
}
