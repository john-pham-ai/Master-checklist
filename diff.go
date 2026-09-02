package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Nightly diff summary: commits between the previous tag of the same family
// (e.g. trucking-scheduled-night-2026-08-31) and the selected tag, classified
// into the areas the smoke test cares about (HMI, Behavior, Planner,
// Prediction) plus bug fixes/reverts, using the paths each commit touched and
// keywords in its title.

var (
	prNumberRe   = regexp.MustCompile(`\(#(\d+)\)\s*$`)
	jiraKeyRe    = regexp.MustCompile(`^([A-Z][A-Z0-9]+-\d+)`)
	jiraPrefixRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+\s*[:\-]\s*`)
	bracketTagRe = regexp.MustCompile(`^\[([^\]]{1,40})\]\s*`)
	tagDateKeyRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:-\d+)?$`)
	fixRe        = regexp.MustCompile(`(?i)\b(fix|fixes|fixed|fixing|bug|bugfix|hotfix|regression|crash|crashes|crashing|resolve|resolves|resolved)\b`)
	revertRe     = regexp.MustCompile(`(?i)\brevert`)
	// genericTitleRe matches housekeeping commits that carry no information for a tester.
	genericTitleRe = regexp.MustCompile(`(?i)^((update|bump|upgrade)\b.*\b(lockfiles?|dependenc(y|ies)|requirements|version)\b.*|\[auto\].*|merge (branch|pull request).*|automated .*|auto[- ]?update.*)$`)
)

type diffArea struct {
	Key          string
	Label        string
	pathPrefixes []string       // longest matching prefix wins across areas
	titleRe      *regexp.Regexp // keyword fallback on the commit title
}

// diffAreas are ordered as they appear in the UI and the Confluence page.
// Paths were mapped from the brain2 tree (Sep 2026).
var diffAreas = []diffArea{
	{Key: "hmi", Label: "HMI",
		pathPrefixes: []string{"onroad/hmi/", "trucking/hmi/", "vehicle_os/hmi/"},
		titleRe:      regexp.MustCompile(`(?i)\bhmi\b`)},
	{Key: "behavior", Label: "Behavior",
		pathPrefixes: []string{"onroad/behavior/", "common/behavior/", "onroad/ml/behavior/", "trucking/fallback/behavior"},
		titleRe:      regexp.MustCompile(`(?i)\bbehaviou?r\b`)},
	{Key: "planner", Label: "Planner",
		pathPrefixes: []string{"onroad/behavior/planning/", "onroad/cmas/planning/", "trucking/planning/", "trucking/fallback/planning", "trucking/interfaces/cmas_planner_nodes/", "trucking/interfaces/sds_planner_nodes/"},
		titleRe:      regexp.MustCompile(`(?i)\b(planner|planning)\b`)},
	{Key: "prediction", Label: "Prediction",
		pathPrefixes: []string{"onroad/behavior/prediction/", "onroad/ml_optimization/prediction/"},
		titleRe:      regexp.MustCompile(`(?i)\bpredict(ion|ions|or|ors)?\b`)},
}

const (
	diffCacheTTL        = 30 * time.Minute
	maxDiffPages        = 10  // x100 commits
	maxClassifyCommits  = 200 // per-commit file lookups beyond this are skipped (title-only)
	classifyConcurrency = 8
	vehicleOSTitle      = "Vehicle OS Change"
)

// Impact: does a change run on the truck, and would a tester notice it?
const (
	impactVisible  = "visible"  // driver-facing: HMI screens/sounds, start-up, health monitor
	impactDriving  = "driving"  // changes how the truck drives: behavior, planner, controls, fallback, maps, vehicle config
	impactInternal = "internal" // runs on the truck but hard to spot: perception, localization, ML, drivers, Vehicle OS
	impactOff      = "off"      // not on the truck: tools, simulation, offboard, dashboards, CI, tests, docs
	impactUnknown  = ""         // touched files not known (title-only classification)
)

// impactOrder is the display/priority order: a commit with any driver-visible
// file is "visible", else any driving file -> "driving", etc.
var impactOrder = []string{impactVisible, impactDriving, impactInternal, impactOff}

var impactPrefixes = map[string][]string{
	impactVisible: {
		"onroad/hmi/", "trucking/hmi/", "vehicle_os/hmi/", "trucking/start_stack/", "trucking/health_monitor/",
	},
	impactDriving: {
		"onroad/behavior/", "onroad/cmas/", "onroad/controls/", "onroad/parking/", "onroad/remote_assistance/", "onroad/mission_manager/", "onroad/config/",
		"trucking/planning/", "trucking/control/", "trucking/fallback/", "trucking/mrm_arbiter/", "trucking/remote_assistance/", "trucking/remote_bridge/",
		"trucking/vehicle_interfaces/", "trucking/config/", "trucking/mapping/",
		"common/behavior/", "common/controls_arbiter/", "common/mission_manager/", "common/drive_by_wire/", "common/adp_map_tiles/",
	},
	impactOff: {
		".buildkite/", ".github/", ".claude/", ".cursor/", "docker/", "cloud/",
		"onroad/tools/", "onroad/data_collection/", "onroad/ml_optimization/", "onroad/visualization/", "onroad/repository_rules/",
		"trucking/tools/", "trucking/offboard/", "trucking/simulation/", "trucking/dashboards/", "trucking/ci/", "trucking/scripts/",
		"trucking/repository_rules/", "trucking/fuzz_tester/", "trucking/dead_code/", "trucking/bazel", "trucking/docker/", "trucking/.config/",
		"common/tools/", "common/offboard/", "common/simulation/", "common/flyte/", "common/data_engine/", "common/dora/",
		"common/sim_trace_uploader/", "common/staging/", "common/orchestration/", "common/foxglove/", "common/data_utils/",
		"common/disk_dump_utils/", "common/repository_rules/",
		"vehicle_os/tools/", "vehicle_os/docker/", "vehicle_os/repository_rules/", "vehicle_os/.config/",
	},
}

var testPathRe = regexp.MustCompile(`(_test\.[a-z]+$|/tests?/|/testdata/|_tests?\.py$|\.md$|/BUILD(\.bazel)?$|/WORKSPACE|\.bzl$|MODULE\.bazel)`)

// impactForPath classifies one file; "" means neutral (tests, docs, build glue).
func impactForPath(p string) string {
	if testPathRe.MatchString(p) {
		return impactUnknown
	}
	best, bestLen := "", 0
	for _, class := range impactOrder {
		for _, pre := range impactPrefixes[class] {
			if strings.HasPrefix(p, pre) && len(pre) > bestLen {
				best, bestLen = class, len(pre)
			}
		}
	}
	if best != "" {
		return best
	}
	// Anything else under the on-vehicle trees runs on the truck but is not driver-facing.
	for _, tree := range []string{"onroad/", "trucking/", "common/", "vehicle_os/"} {
		if strings.HasPrefix(p, tree) {
			return impactInternal
		}
	}
	return impactOff
}

// simTitleRe spots simulation-only work whose files live in generic on-vehicle
// trees (e.g. a "[Sim] ... LogSim" change under trucking/autonomy_platform).
var simTitleRe = regexp.MustCompile(`(?i)(\[sim\]|\blogsim\b|\bsimulation\b|\bsim[- ]only\b|\bresim\b)`)

// refineImpact downgrades an "internal" verdict to "off" when the title makes
// clear the change is simulation tooling. Visible/driving verdicts are kept.
func refineImpact(impact, title string) string {
	if impact == impactInternal && simTitleRe.MatchString(title) {
		return impactOff
	}
	return impact
}

// classifyImpact picks the commit's impact from its files by priority
// (visible > driving > internal > off). Neutral files are ignored; a commit
// with only neutral files is "off" (tests/docs/build glue).
func classifyImpact(paths []string) string {
	seen := map[string]bool{}
	for _, p := range paths {
		if c := impactForPath(p); c != "" {
			seen[c] = true
		}
	}
	for _, class := range impactOrder {
		if seen[class] {
			return class
		}
	}
	if len(paths) > 0 {
		return impactOff
	}
	return impactUnknown
}

type diffCommit struct {
	SHA         string   `json:"sha"`
	Title       string   `json:"title"`          // raw first line
	Headline    string   `json:"headline"`       // title without Jira key / PR number / bracket tags
	Tags        []string `json:"tags,omitempty"` // leading bracket tags, e.g. Trucking, CMAS
	Described   bool     `json:"described"`      // false for automated/housekeeping commits
	Summary     string   `json:"summary,omitempty"`
	Excerpt     string   `json:"excerpt,omitempty"`     // first sentences of the PR description (behavior changes)
	PlainTitle  string   `json:"plain_title,omitempty"` // headline with jargon swapped for plain words
	Kind        string   `json:"kind,omitempty"`        // fix | revert | speedup | tuning | refactor | new | removal | other
	Note        string   `json:"note,omitempty"`        // English "what this means for the truck" (on-truck changes)
	NeedsInfo   bool     `json:"needs_info"`            // affects the truck but has no usable description
	PR          int      `json:"pr,omitempty"`
	PRURL       string   `json:"pr_url,omitempty"` // https://github.com/<owner>/<repo>/pull/<PR>
	Jira        string   `json:"jira,omitempty"`
	URL         string   `json:"url"` // commit page on GitHub
	Author      string   `json:"author,omitempty"`
	Date        string   `json:"date,omitempty"`
	Areas       []string `json:"areas"`
	Impact      string   `json:"impact"` // visible | driving | internal | off | "" (unknown)
	IsFix       bool     `json:"is_fix"`
	IsRevert    bool     `json:"is_revert"`
	IsVehicleOS bool     `json:"is_vehicle_os"`
	Origin      string   `json:"origin,omitempty"` // GitOrigin-RevId of an automatic sync (commit in the source repo)
	Dirs        []string `json:"dirs,omitempty"`   // most-touched top-level dirs, e.g. onroad/behavior
	Files       []string `json:"files,omitempty"`  // representative touched files (tracked areas first)
	FilesKnown  bool     `json:"files_known"`
}

const maxFilesPerCommit = 8

var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

type diffCategory struct {
	Key                string         `json:"key"`
	Label              string         `json:"label"`
	Items              []diffCommit   `json:"items"`               // described on-truck commits only
	Undescribed        int            `json:"undescribed"`         // automated/housekeeping on-truck commits (count only)
	UndescribedDriving int            `json:"undescribed_driving"` // of those, how many touched driving/driver-facing code (flagged)
	Flagged            []diffCommit   `json:"flagged"`             // those undescribed driving/driver-facing commits (no PR; commit + files)
	Impact             map[string]int `json:"impact"`              // counts by impact class over on-truck commits in this area
}

var gitOriginRe = regexp.MustCompile(`GitOrigin-RevId:\s*([0-9a-f]{7,40})`)

// simpleArea / simpleLang hold the plain-language ("explain it to a 10-year-old")
// rendering of a diff in one language, written by the model when Vertex AI is
// available (translator.simplifyDiff). Without it the browser and the
// Confluence renderer fall back to fixed sentence templates plus the change
// headlines.
type simpleArea struct {
	Sentence string   `json:"sentence"`
	Bullets  []string `json:"bullets"`
}

type simpleLang struct {
	Overall string                `json:"overall"`
	Areas   map[string]simpleArea `json:"areas"` // keyed by category key: hmi, behavior, planner, prediction, fixes
}

type diffSummary struct {
	Repo           string                `json:"repo"`
	Base           string                `json:"base"`
	Head           string                `json:"head"`
	BaseDate       string                `json:"base_date,omitempty"` // YYYY-MM-DD(-NN) from the tag
	HeadDate       string                `json:"head_date,omitempty"`
	CompareURL     string                `json:"compare_url"`
	TotalCommits   int                   `json:"total_commits"`
	Categories     []diffCategory        `json:"categories"`      // HMI, Behavior, Planner, Prediction, Bug fixes
	OtherCount     int                   `json:"other_count"`     // on-truck commits outside every category (count only)
	OtherAutomated int                   `json:"other_automated"` // of which Vehicle OS syncs
	Impact         map[string]int        `json:"impact"`          // counts by impact class over all commits (incl. ignored "off")
	Ignored        int                   `json:"ignored"`         // "off" commits (tools/sim/tests) — not shown anywhere else
	OnTruck        int                   `json:"on_truck"`        // commits that run on the truck (visible+driving+internal+unknown)
	Truncated      bool                  `json:"truncated"`
	Simple         map[string]simpleLang `json:"simple,omitempty"` // "en", "ja" — model-written plain language
	AISummary      string                `json:"ai_summary,omitempty"`
	Note           string                `json:"note,omitempty"`
	GeneratedAt    string                `json:"generated_at"`
}

type diffService struct {
	cfg  config
	tags *tagCache
	tr   *translator

	mu    sync.Mutex
	cache map[string]cachedDiff
}

type cachedDiff struct {
	summary *diffSummary
	at      time.Time
}

func newDiffService(cfg config, tags *tagCache, tr *translator) *diffService {
	return &diffService{cfg: cfg, tags: tags, tr: tr, cache: map[string]cachedDiff{}}
}

// tagFamily strips the trailing YYYY-MM-DD(-NN) stamp: "trucking-candidate-".
func tagFamily(tag string) string { return tagDateKeyRe.ReplaceAllString(tag, "") }

func tagDateKey(tag string) string { return tagDateKeyRe.FindString(tag) }

// previousTag returns the tag of the same family with the greatest date key
// strictly before head's, or "" if none.
func previousTag(tags []string, head string) string {
	fam, key := tagFamily(head), tagDateKey(head)
	if key == "" {
		return ""
	}
	best, bestKey := "", ""
	for _, t := range tags {
		if t == head || tagFamily(t) != fam {
			continue
		}
		k := tagDateKey(t)
		if k == "" || k >= key {
			continue
		}
		if k > bestKey {
			best, bestKey = t, k
		}
	}
	return best
}

// GET /api/diff?head=<tag>[&base=<tag>]
func (s *diffService) handle(w http.ResponseWriter, r *http.Request) {
	head := strings.TrimSpace(r.URL.Query().Get("head"))
	base := strings.TrimSpace(r.URL.Query().Get("base"))
	if head == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing head tag"})
		return
	}
	if s.cfg.DryRun {
		if base == "" {
			base = previousTag(dryRunSampleTags, head)
		}
		writeJSON(w, http.StatusOK, dryRunDiff(base, head))
		return
	}

	tags, err := s.tags.Get(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not list tags: " + err.Error()})
		return
	}
	if !containsString(tags, head) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("unknown tag %q", head)})
		return
	}
	if base == "" {
		base = previousTag(tags, head)
		if base == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no earlier tag of the same kind to compare against; enter one manually"})
			return
		}
	} else if !containsString(tags, base) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("unknown base tag %q", base)})
		return
	}

	summary, err := s.get(r.Context(), base, head)
	if err != nil {
		log.Printf("diff %s...%s: %v", base, head, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *diffService) get(ctx context.Context, base, head string) (*diffSummary, error) {
	key := base + "..." + head
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && time.Since(c.at) < diffCacheTTL {
		s.mu.Unlock()
		return c.summary, nil
	}
	s.mu.Unlock()

	token, err := s.cfg.GithubToken.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("github token: %w", err)
	}
	summary, err := buildDiffSummary(ctx, s.cfg.GithubOwner, s.cfg.GithubRepo, token, base, head)
	if err != nil {
		return nil, err
	}
	if s.tr != nil && !s.tr.disabled {
		if simple, err := s.tr.simplifyDiff(ctx, summary); err != nil {
			log.Printf("diff: plain-language summary unavailable: %v", err)
			summary.Note = "Plain-language summary unavailable: " + truncate(err.Error(), 120)
		} else {
			summary.Simple = simple
			if en, ok := simple["en"]; ok {
				summary.AISummary = en.Overall
			}
		}
	}

	s.mu.Lock()
	s.cache[key] = cachedDiff{summary: summary, at: time.Now()}
	s.mu.Unlock()
	return summary, nil
}

type ghCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Files []struct {
		Filename string `json:"filename"`
	} `json:"files"`
}

type ghCompare struct {
	TotalCommits int        `json:"total_commits"`
	HTMLURL      string     `json:"html_url"`
	Commits      []ghCommit `json:"commits"`
}

func buildDiffSummary(ctx context.Context, owner, repo, token, base, head string) (*diffSummary, error) {
	var all []ghCommit
	var compareURL string
	total := -1
	for page := 1; page <= maxDiffPages; page++ {
		u := fmt.Sprintf("https://api.github.com/repos/%s/%s/compare/%s...%s?per_page=100&page=%d", owner, repo, base, head, page)
		var cmp ghCompare
		if err := githubGetJSON(ctx, token, u, &cmp); err != nil {
			return nil, fmt.Errorf("compare %s...%s: %w", base, head, err)
		}
		if page == 1 {
			compareURL, total = cmp.HTMLURL, cmp.TotalCommits
		}
		all = append(all, cmp.Commits...)
		if len(cmp.Commits) == 0 || len(all) >= total {
			break
		}
	}

	items := make([]diffCommit, len(all))
	for i, c := range all {
		items[i] = parseCommit(c)
		if items[i].PR > 0 {
			items[i].PRURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, items[i].PR)
		}
	}

	// Fetch touched files per commit (bounded) to classify by path.
	truncated := len(items) > maxClassifyCommits
	sem := make(chan struct{}, classifyConcurrency)
	var wg sync.WaitGroup
	for i := range items {
		if i >= maxClassifyCommits {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var c ghCommit
			u := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, items[i].SHA)
			if err := githubGetJSON(ctx, token, u, &c); err != nil {
				return // title-only classification for this commit
			}
			paths := make([]string, 0, len(c.Files))
			for _, f := range c.Files {
				paths = append(paths, f.Filename)
			}
			applyPaths(&items[i], paths)
		}(i)
	}
	wg.Wait()

	summary := &diffSummary{
		Repo: owner + "/" + repo, Base: base, Head: head, CompareURL: compareURL,
		BaseDate: tagDateKey(base), HeadDate: tagDateKey(head),
		TotalCommits: len(items), Truncated: truncated,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	summary.Categories, summary.OtherCount, summary.OtherAutomated = categorize(items)
	summary.Impact = impactCounts(items)
	summary.Ignored = summary.Impact[impactOff]
	summary.OnTruck = summary.TotalCommits - summary.Ignored
	return summary, nil
}

// parseCommit extracts title, PR number, Jira key, summary and title-based flags.
func parseCommit(c ghCommit) diffCommit {
	msg := strings.ReplaceAll(c.Commit.Message, "\r\n", "\n")
	lines := strings.Split(msg, "\n")
	title := strings.TrimSpace(lines[0])
	// PR templates leave HTML comments in the body; drop them before picking a summary line.
	body := strings.Split(htmlCommentRe.ReplaceAllString(strings.Join(lines[1:], "\n"), ""), "\n")
	item := diffCommit{
		SHA: c.SHA, Title: title, URL: c.HTMLURL,
		Author: c.Commit.Author.Name, Date: c.Commit.Author.Date,
		Areas: []string{},
	}
	if m := prNumberRe.FindStringSubmatch(title); m != nil {
		fmt.Sscanf(m[1], "%d", &item.PR)
	}
	if m := jiraKeyRe.FindStringSubmatch(title); m != nil {
		item.Jira = m[1]
	}
	item.IsVehicleOS = title == vehicleOSTitle
	if m := gitOriginRe.FindStringSubmatch(msg); m != nil {
		item.Origin = m[1]
	}
	item.IsRevert = revertRe.MatchString(title)
	item.IsFix = fixRe.MatchString(title) || item.IsRevert
	item.Summary = firstSummaryLine(body)
	item.Excerpt = extractExcerpt(body)
	item.Headline, item.Tags = humanTitle(title)
	item.Described = hasDescription(item)
	if item.Described {
		item.PlainTitle = plainTitle(item.Headline)
		item.Kind = changeKind(item.Headline, item.Excerpt)
	}
	for _, a := range diffAreas {
		if a.titleRe.MatchString(title) {
			item.Areas = appendUnique(item.Areas, a.Key)
		}
	}
	return item
}

// humanTitle turns
//
//	"FRONTIER-34904: [Trucking][CMAS] Replace lane-topology tracking regions (#120052)"
//
// into ("Replace lane-topology tracking regions", ["Trucking", "CMAS"]).
func humanTitle(title string) (string, []string) {
	s := strings.TrimSpace(title)
	s = prNumberRe.ReplaceAllString(s, "")
	s = jiraPrefixRe.ReplaceAllString(s, "")
	var tags []string
	for {
		m := bracketTagRe.FindStringSubmatch(s)
		if m == nil {
			break
		}
		tags = append(tags, strings.TrimSpace(m[1]))
		s = s[len(m[0]):]
	}
	s = strings.TrimSpace(strings.TrimRight(s, " .:-"))
	if s == "" {
		return "", tags
	}
	// Revert "FRONTIER-1: [Sim] Re-enable X" -> Reverted: Re-enable X  (tags: Sim)
	if m := revertQuoteRe.FindStringSubmatch(s); m != nil {
		inner, innerTags := humanTitle(m[1])
		if inner != "" {
			return "Reverted: " + inner, append(tags, innerTags...)
		}
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r), tags
}

var revertQuoteRe = regexp.MustCompile(`(?i)^revert\s+["“](.+)["”]$`)

// hasDescription reports whether a commit tells a tester anything: automated
// syncs and housekeeping bumps do not.
func hasDescription(item diffCommit) bool {
	if item.IsVehicleOS {
		return false
	}
	h := strings.TrimSpace(item.Headline)
	if h == "" || genericTitleRe.MatchString(h) || genericTitleRe.MatchString(item.Title) {
		return false
	}
	return true
}

var (
	mdLinkRe       = regexp.MustCompile(`\[[^\]]*\]\([^)]*\)`) // [text](url) -> dropped (link words carry no meaning)
	danglingLinkRe = regexp.MustCompile(`\[[^\]]*\]\s*\(?`)    // [text]( left over from a wrapped link
	bareURLRe      = regexp.MustCompile(`https?://\S+`)
	jiraInTextRe   = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b[;:,]?`)
	leadingJunkRe  = regexp.MustCompile(`^[\s;:,.()\-–—]+`)
	sentenceEnd    = regexp.MustCompile(`([.!?])\s+`)
)

// extractExcerpt returns the first few sentences of a PR description in
// plain text (max ~420 chars): headers, images, links, checklists, template
// labels and trailers are dropped.
func extractExcerpt(body []string) string {
	var kept []string
	for _, raw := range body {
		l := strings.TrimSpace(raw)
		switch {
		case l == "", strings.HasPrefix(l, "#"), strings.HasPrefix(l, "<"), strings.HasPrefix(l, "!["),
			strings.HasPrefix(l, "GitOrigin-RevId"), strings.HasPrefix(l, "Co-authored-by"), strings.HasPrefix(l, "Signed-off-by"),
			strings.HasPrefix(l, "---"), strings.HasPrefix(l, "|"), strings.HasPrefix(l, "- [ ]"), strings.HasPrefix(l, "- [x]"),
			strings.Contains(l, "-->"), strings.Contains(l, "<!--"), strings.HasPrefix(l, "src="), strings.HasPrefix(l, "/>"):
			continue
		}
		l = strings.NewReplacer("**", "", "`", "", "* ", "", "- ", "").Replace(l)
		l = strings.TrimSpace(l)
		if l == "" || (strings.HasSuffix(l, ":") && len(strings.Fields(l)) <= 3) {
			continue
		}
		kept = append(kept, l)
		if len(strings.Join(kept, " ")) > 800 {
			break
		}
	}
	// Clean on the joined text so links wrapped across lines are caught too.
	text := strings.Join(kept, " ")
	text = mdLinkRe.ReplaceAllString(text, "")
	text = bareURLRe.ReplaceAllString(text, "")
	text = danglingLinkRe.ReplaceAllString(text, "")
	text = jiraInTextRe.ReplaceAllString(text, "")
	text = multiSpaceRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(leadingJunkRe.ReplaceAllString(strings.TrimSpace(text), ""))
	if text == "" {
		return ""
	}
	// Keep up to three sentences.
	parts := sentenceEnd.Split(text, -1)
	ends := sentenceEnd.FindAllStringSubmatch(text, -1)
	var b strings.Builder
	for i, p := range parts {
		if i == 3 || strings.TrimSpace(p) == "" {
			break
		}
		b.WriteString(strings.TrimSpace(p))
		if i < len(ends) {
			b.WriteString(ends[i][1])
		}
		b.WriteString(" ")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = text
	}
	return truncateRunes(out, 420)
}

// firstSummaryLine picks the first informative body line of a PR description.
func firstSummaryLine(body []string) string {
	for _, raw := range body {
		l := strings.TrimSpace(raw)
		switch {
		case l == "", strings.HasPrefix(l, "#"), strings.HasPrefix(l, "<"), strings.HasPrefix(l, "!["),
			strings.HasPrefix(l, "GitOrigin-RevId"), strings.HasPrefix(l, "Co-authored-by"), strings.HasPrefix(l, "Signed-off-by"),
			strings.HasPrefix(l, "---"), strings.HasPrefix(l, "|"), strings.HasPrefix(l, "- [ ]"), strings.HasPrefix(l, "- [x]"),
			strings.Contains(l, "-->"), strings.Contains(l, "<!--"):
			continue
		}
		l = strings.NewReplacer("**", "", "`", "").Replace(l)
		// Template labels such as "Context:" or "Summary:" are not descriptions.
		if strings.HasSuffix(l, ":") && len(strings.Fields(l)) <= 3 {
			continue
		}
		return truncateRunes(l, 180)
	}
	return ""
}

// applyPaths classifies a commit by the files it touched and records the
// most-touched top-level directories.
func applyPaths(item *diffCommit, paths []string) {
	item.FilesKnown = true
	item.Impact = refineImpact(classifyImpact(paths), item.Title)
	finalizeImpact(item)
	dirCount := map[string]int{}
	var tracked, untracked []string
	for _, p := range paths {
		if key := areaForPath(p); key != "" {
			item.Areas = appendUnique(item.Areas, key)
			tracked = append(tracked, p)
		} else {
			untracked = append(untracked, p)
		}
		parts := strings.SplitN(p, "/", 3)
		dir := parts[0]
		if len(parts) > 1 {
			dir = parts[0] + "/" + parts[1]
		}
		dirCount[dir]++
	}
	type kv struct {
		k string
		n int
	}
	var dirs []kv
	for k, n := range dirCount {
		dirs = append(dirs, kv{k, n})
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].n != dirs[j].n {
			return dirs[i].n > dirs[j].n
		}
		return dirs[i].k < dirs[j].k
	})
	item.Dirs = item.Dirs[:0]
	for i, d := range dirs {
		if i == 4 {
			break
		}
		item.Dirs = append(item.Dirs, d.k)
	}
	// Representative files: those in tracked areas first, so an opaque
	// "Vehicle OS Change" that touches onroad/behavior shows what moved.
	item.Files = item.Files[:0]
	for _, p := range append(tracked, untracked...) {
		if len(item.Files) == maxFilesPerCommit {
			break
		}
		item.Files = append(item.Files, p)
	}
	// Keep area order stable (UI order).
	sort.Slice(item.Areas, func(i, j int) bool { return areaIndex(item.Areas[i]) < areaIndex(item.Areas[j]) })
}

// affectsTruckBehavior reports whether a change is one a tester could notice.
func affectsTruckBehavior(impact string) bool {
	return impact == impactVisible || impact == impactDriving
}

// finalizeImpact derives the behavior note and the needs-info flag once the
// impact class is known.
func finalizeImpact(item *diffCommit) {
	if !affectsTruckBehavior(item.Impact) {
		item.Note, item.NeedsInfo = "", false
		return
	}
	if item.Described {
		item.Note = behaviorNote(item.Kind, item.Impact)
	}
	item.NeedsInfo = !item.Described || strings.TrimSpace(item.Excerpt) == ""
}

// areaForPath returns the area whose longest path prefix matches p, or "".
func areaForPath(p string) string {
	best, bestLen := "", 0
	for _, a := range diffAreas {
		for _, pre := range a.pathPrefixes {
			if strings.HasPrefix(p, pre) && len(pre) > bestLen {
				best, bestLen = a.Key, len(pre)
			}
		}
	}
	return best
}

func areaIndex(key string) int {
	for i, a := range diffAreas {
		if a.Key == key {
			return i
		}
	}
	return len(diffAreas)
}

// categorize builds the categories (one per area, then bug fixes/reverts).
// Only described commits are listed; automated/housekeeping commits that
// touched an area are counted in Undescribed. Commits outside every area are
// returned as counts only.
func categorize(items []diffCommit) (cats []diffCategory, otherCount, otherAutomated int) {
	cats = make([]diffCategory, 0, len(diffAreas)+1)
	for _, a := range diffAreas {
		cats = append(cats, diffCategory{Key: a.Key, Label: a.Label, Items: []diffCommit{}, Flagged: []diffCommit{}, Impact: map[string]int{}})
	}
	cats = append(cats, diffCategory{Key: "fixes", Label: "Bug fixes & reverts", Items: []diffCommit{}, Flagged: []diffCommit{}, Impact: map[string]int{}})
	fixes := &cats[len(cats)-1]

	add := func(c *diffCategory, it diffCommit) {
		if it.Described {
			c.Items = append(c.Items, it)
		} else {
			c.Undescribed++
			if affectsTruckBehavior(it.Impact) {
				c.UndescribedDriving++
				c.Flagged = append(c.Flagged, it)
			}
		}
		c.Impact[impactKey(it.Impact)]++
	}
	for _, it := range items {
		if it.Impact == impactOff {
			continue // tools / simulation / tests: ignored (counted in diffSummary.Ignored)
		}
		for _, key := range it.Areas {
			add(&cats[areaIndex(key)], it)
		}
		if it.IsFix {
			add(fixes, it)
		}
		if len(it.Areas) == 0 && !it.IsFix {
			otherCount++
			if it.IsVehicleOS {
				otherAutomated++
			}
		}
	}
	// Behavior-affecting changes first within each area, then the rest.
	for i := range cats {
		sort.SliceStable(cats[i].Items, func(a, b int) bool {
			ra, rb := impactRank(cats[i].Items[a].Impact), impactRank(cats[i].Items[b].Impact)
			return ra < rb
		})
	}
	return cats, otherCount, otherAutomated
}

func impactRank(impact string) int {
	for i, c := range impactOrder {
		if c == impact {
			return i
		}
	}
	return len(impactOrder)
}

// impactKey maps the empty (unknown) impact to a JSON-friendly key.
func impactKey(impact string) string {
	if impact == "" {
		return "unknown"
	}
	return impact
}

// impactCounts tallies commits by impact class.
func impactCounts(items []diffCommit) map[string]int {
	m := map[string]int{}
	for _, it := range items {
		m[impactKey(it.Impact)]++
	}
	return m
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// dryRunDiff returns a fixed sample so the UI can be exercised without GitHub.
func dryRunDiff(base, head string) *diffSummary {
	mk := func(title, summary string, areas []string, fix, vos bool, dirs ...string) diffCommit {
		gc := ghCommit{SHA: "0123456789abcdef", HTMLURL: "https://github.com/Ext-Applied-Frontier/brain2/commit/0123456"}
		gc.Commit.Message = title + "\n\n" + summary
		c := parseCommit(gc)
		if c.PR > 0 {
			c.PRURL = fmt.Sprintf("https://github.com/Ext-Applied-Frontier/brain2/pull/%d", c.PR)
		}
		c.Areas, c.IsFix, c.IsVehicleOS, c.Dirs, c.FilesKnown = areas, fix || c.IsFix, vos, dirs, true
		c.Described = hasDescription(c)
		var paths []string
		for _, d := range dirs {
			paths = append(paths, d+"/sample.cc")
		}
		c.Impact = refineImpact(classifyImpact(paths), c.Title)
		finalizeImpact(&c)
		return c
	}
	items := []diffCommit{
		mk("FRONTIER-35010: [HMI] Show remaining ODD distance on driver display (#121001)", "Adds a countdown widget next to the AD/MD indicator.", []string{"hmi"}, false, false, "onroad/hmi"),
		mk("FRONTIER-34980: Fix planner stall when route has back-to-back merges (#120950)", "Regression from #120410; adds sim coverage.", []string{"planner"}, true, false, "onroad/behavior/planning"),
		mk("FRONTIER-34877: Retune cut-in prediction horizon for trucks (#120880)", "", []string{"prediction"}, false, false, "onroad/behavior/prediction"),
		mk("FRONTIER-34901: [Behavior] Yield earlier to emergency vehicles (#120902)", "", []string{"behavior"}, false, false, "onroad/behavior"),
		mk("FRONTIER-34990: [Sim] Add planner regression scenario for merges (#120960)", "Simulation only.", []string{"planner"}, false, false, "trucking/simulation"),
		mk("Vehicle OS Change", "", []string{}, false, true, "vehicle_os/third_party"),
		mk("FRONTIER-34960: Update usa_zone_10 HD map data (#120937)", "Basemap and routes update.", []string{}, false, false, "trucking/mapping"),
	}
	d := &diffSummary{
		Repo: "Ext-Applied-Frontier/brain2", Base: base, Head: head,
		BaseDate: tagDateKey(base), HeadDate: tagDateKey(head),
		CompareURL:   "https://github.com/Ext-Applied-Frontier/brain2/compare/" + base + "..." + head,
		TotalCommits: len(items),
		AISummary:    "Dry run: a small build. The driver's screen shows how far the truck can keep driving itself, a planner problem was fixed, and the truck got better at guessing cut-ins.",
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	d.Categories, d.OtherCount, d.OtherAutomated = categorize(items)
	d.Impact = impactCounts(items)
	d.Ignored = d.Impact[impactOff]
	d.OnTruck = d.TotalCommits - d.Ignored
	d.Simple = map[string]simpleLang{
		"en": {Overall: d.AISummary, Areas: map[string]simpleArea{
			"hmi":        {Sentence: "1 change to what the driver sees on the screen.", Bullets: []string{"The screen now shows how far the truck can keep driving itself."}},
			"behavior":   {Sentence: "1 change to how the truck decides what to do.", Bullets: []string{"The truck moves out of the way of ambulances and fire trucks sooner."}},
			"planner":    {Sentence: "1 change to how the truck plans where to drive.", Bullets: []string{"You might notice: fixed the truck getting stuck when two merges come right after each other."}},
			"prediction": {Sentence: "1 change to how the truck guesses what other cars will do.", Bullets: []string{"Better at guessing when a car will cut in front of the truck."}},
			"fixes":      {Sentence: "1 problem was fixed.", Bullets: []string{"Fixed the truck getting stuck when two merges come right after each other."}},
		}},
	}
	return d
}

// parseDiffJSON decodes the summary the browser posts back with the form.
func parseDiffJSON(s string) (*diffSummary, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var d diffSummary
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return nil, err
	}
	if d.Head == "" || d.Base == "" {
		return nil, fmt.Errorf("diff json missing base/head")
	}
	return &d, nil
}
