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

type diffCommit struct {
	SHA         string   `json:"sha"`
	Title       string   `json:"title"`          // raw first line
	Headline    string   `json:"headline"`       // title without Jira key / PR number / bracket tags
	Tags        []string `json:"tags,omitempty"` // leading bracket tags, e.g. Trucking, CMAS
	Described   bool     `json:"described"`      // false for automated/housekeeping commits
	Summary     string   `json:"summary,omitempty"`
	PR          int      `json:"pr,omitempty"`
	Jira        string   `json:"jira,omitempty"`
	URL         string   `json:"url"`
	Author      string   `json:"author,omitempty"`
	Date        string   `json:"date,omitempty"`
	Areas       []string `json:"areas"`
	IsFix       bool     `json:"is_fix"`
	IsRevert    bool     `json:"is_revert"`
	IsVehicleOS bool     `json:"is_vehicle_os"`
	Dirs        []string `json:"dirs,omitempty"`  // most-touched top-level dirs, e.g. onroad/behavior
	Files       []string `json:"files,omitempty"` // representative touched files (tracked areas first)
	FilesKnown  bool     `json:"files_known"`
}

const maxFilesPerCommit = 8

var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

type diffCategory struct {
	Key         string       `json:"key"`
	Label       string       `json:"label"`
	Items       []diffCommit `json:"items"`       // described commits only
	Undescribed int          `json:"undescribed"` // automated/housekeeping commits that touched this area (count only)
}

type diffSummary struct {
	Repo           string         `json:"repo"`
	Base           string         `json:"base"`
	Head           string         `json:"head"`
	BaseDate       string         `json:"base_date,omitempty"` // YYYY-MM-DD(-NN) from the tag
	HeadDate       string         `json:"head_date,omitempty"`
	CompareURL     string         `json:"compare_url"`
	TotalCommits   int            `json:"total_commits"`
	Categories     []diffCategory `json:"categories"`      // HMI, Behavior, Planner, Prediction, Bug fixes
	OtherCount     int            `json:"other_count"`     // commits outside every category (count only)
	OtherAutomated int            `json:"other_automated"` // of which Vehicle OS syncs
	Truncated      bool           `json:"truncated"`
	AISummary      string         `json:"ai_summary,omitempty"`
	Note           string         `json:"note,omitempty"`
	GeneratedAt    string         `json:"generated_at"`
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
		if ai, err := s.tr.summarizeDiff(ctx, summary); err != nil {
			log.Printf("diff: AI summary unavailable: %v", err)
			summary.Note = "AI summary unavailable: " + truncate(err.Error(), 120)
		} else {
			summary.AISummary = ai
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
	item.IsRevert = revertRe.MatchString(title)
	item.IsFix = fixRe.MatchString(title) || item.IsRevert
	item.Summary = firstSummaryLine(body)
	item.Headline, item.Tags = humanTitle(title)
	item.Described = hasDescription(item)
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
		cats = append(cats, diffCategory{Key: a.Key, Label: a.Label, Items: []diffCommit{}})
	}
	cats = append(cats, diffCategory{Key: "fixes", Label: "Bug fixes & reverts", Items: []diffCommit{}})
	fixes := &cats[len(cats)-1]

	add := func(c *diffCategory, it diffCommit) {
		if it.Described {
			c.Items = append(c.Items, it)
		} else {
			c.Undescribed++
		}
	}
	for _, it := range items {
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
	return cats, otherCount, otherAutomated
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
		c.Areas, c.IsFix, c.IsVehicleOS, c.Dirs, c.FilesKnown = areas, fix || c.IsFix, vos, dirs, true
		c.Described = hasDescription(c)
		return c
	}
	items := []diffCommit{
		mk("FRONTIER-35010: [HMI] Show remaining ODD distance on driver display (#121001)", "Adds a countdown widget next to the AD/MD indicator.", []string{"hmi"}, false, false, "onroad/hmi"),
		mk("FRONTIER-34980: Fix planner stall when route has back-to-back merges (#120950)", "Regression from #120410; adds sim coverage.", []string{"planner"}, true, false, "onroad/behavior/planning"),
		mk("FRONTIER-34877: Retune cut-in prediction horizon for trucks (#120880)", "", []string{"prediction"}, false, false, "onroad/behavior/prediction"),
		mk("FRONTIER-34901: [Behavior] Yield earlier to emergency vehicles (#120902)", "", []string{"behavior"}, false, false, "onroad/behavior"),
		mk("Vehicle OS Change", "", []string{}, false, true, "vehicle_os/third_party"),
		mk("FRONTIER-34960: Update usa_zone_10 HD map data (#120937)", "Basemap and routes update.", []string{}, false, false, "trucking/mapping"),
	}
	d := &diffSummary{
		Repo: "Ext-Applied-Frontier/brain2", Base: base, Head: head,
		BaseDate: tagDateKey(base), HeadDate: tagDateKey(head),
		CompareURL:   "https://github.com/Ext-Applied-Frontier/brain2/compare/" + base + "..." + head,
		TotalCommits: len(items),
		AISummary:    "- Dry run: sample build. The driver display gains a countdown of remaining self-driving distance.\n- A planner problem on back-to-back merges was fixed.\n- Prediction of cut-ins was retuned for trucks.",
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	d.Categories, d.OtherCount, d.OtherAutomated = categorize(items)
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
