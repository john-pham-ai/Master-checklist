package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestPreviousTag(t *testing.T) {
	tags := []string{
		"trucking-scheduled-night-2026-09-01",
		"trucking-scheduled-night-2026-08-31",
		"trucking-scheduled-night-2026-08-30",
		"verified/trucking-scheduled-night-2026-08-31",
		"trucking-candidate-2026-08-26-01",
		"trucking-candidate-2026-08-26-00",
		"trucking-candidate-2026-08-24-00",
		"trucking-scheduled-day-2026-08-31",
		"release/ISZ-2026.29",
	}
	cases := map[string]string{
		"trucking-scheduled-night-2026-09-01":          "trucking-scheduled-night-2026-08-31",
		"trucking-scheduled-night-2026-08-30":          "",
		"trucking-candidate-2026-08-26-01":             "trucking-candidate-2026-08-26-00",
		"trucking-candidate-2026-08-26-00":             "trucking-candidate-2026-08-24-00",
		"verified/trucking-scheduled-night-2026-08-31": "",
		"release/ISZ-2026.29":                          "",
	}
	for head, want := range cases {
		if got := previousTag(tags, head); got != want {
			t.Errorf("previousTag(%q) = %q, want %q", head, got, want)
		}
	}
}

func TestParseCommit(t *testing.T) {
	c := ghCommit{SHA: "abc", HTMLURL: "https://x/commit/abc"}
	c.Commit.Message = "FRONTIER-34975: Revert \"FRONTIER-34267: [Sim] Re-enable CL Planner-only LogSim\" (#120503)\n\n## Summary\n<img src=\"x\"/>\n**Reverting** because it broke the nightly.\n\nGitOrigin-RevId: deadbeef"
	got := parseCommit(c)
	if got.PR != 120503 || got.Jira != "FRONTIER-34975" {
		t.Errorf("pr/jira = %d/%q", got.PR, got.Jira)
	}
	if !got.IsRevert || !got.IsFix {
		t.Errorf("revert should count as fix: %+v", got)
	}
	if got.Summary != "Reverting because it broke the nightly." {
		t.Errorf("summary = %q", got.Summary)
	}
	if !reflect.DeepEqual(got.Areas, []string{"planner"}) {
		t.Errorf("title keyword areas = %v, want [planner]", got.Areas)
	}

	vos := ghCommit{SHA: "def"}
	vos.Commit.Message = "Vehicle OS Change\n\nGitOrigin-RevId: a9bbd36c0e0fe7ddf882bb282575d1d3bd065bdc"
	if p := parseCommit(vos); !p.IsVehicleOS || p.Summary != "" || p.IsFix || p.Origin != "a9bbd36c0e0fe7ddf882bb282575d1d3bd065bdc" {
		t.Errorf("vehicle os parse: %+v", p)
	}

	// PR-template HTML comments (possibly multi-line) must not leak into the summary.
	tmpl := ghCommit{SHA: "ghi"}
	tmpl.Commit.Message = "FRONTIER-33611: [Trucking][Trajectory] Reuse waypoints (#121000)\n\n<!-- Use [NEU-XXX] to mention the issue\nbut keep it open. -->\n## Summary\n- [ ] checklist item\nReuse cached waypoints to cut planner latency.\n"
	if p := parseCommit(tmpl); p.Summary != "Reuse cached waypoints to cut planner latency." {
		t.Errorf("summary with html comment = %q", p.Summary)
	}

	// Template labels ("Context:") are skipped in favour of the first real sentence.
	lbl := ghCommit{SHA: "jkl"}
	lbl.Commit.Message = "FRONTIER-1: Something (#2)\n\nContext:\nThe planner re-computed waypoints every tick.\n"
	if p := parseCommit(lbl); p.Summary != "The planner re-computed waypoints every tick." {
		t.Errorf("summary after label = %q", p.Summary)
	}
}

func TestApplyPathsFiles(t *testing.T) {
	c := diffCommit{Title: vehicleOSTitle, IsVehicleOS: true, Areas: []string{}}
	applyPaths(&c, []string{
		"vehicle_os/middleware/a.cc", "vehicle_os/middleware/b.cc",
		"onroad/behavior/components/yield.cc", "onroad/ml/model.py",
	})
	if len(c.Files) != 4 || c.Files[0] != "onroad/behavior/components/yield.cc" {
		t.Errorf("tracked-area files should come first: %v", c.Files)
	}
	if !reflect.DeepEqual(c.Areas, []string{"behavior"}) {
		t.Errorf("areas = %v", c.Areas)
	}
}

func TestApplyPathsAndCategorize(t *testing.T) {
	a := diffCommit{Title: "x", Areas: []string{}}
	applyPaths(&a, []string{
		"onroad/behavior/planning/lane_change.cc",
		"onroad/behavior/planning/lane_change_test.cc",
		"onroad/behavior/routing/router.cc",
		"vehicle_os/hmi/app/main.kt",
		"onroad/lockfiles/requirements_lock.txt",
	})
	if !reflect.DeepEqual(a.Areas, []string{"hmi", "behavior", "planner"}) {
		t.Errorf("areas = %v, want [hmi behavior planner] (UI order)", a.Areas)
	}
	if a.Dirs[0] != "onroad/behavior" {
		t.Errorf("most-touched dir = %v", a.Dirs)
	}

	a.Described = true
	fix := diffCommit{Title: "Fix crash in prediction", Headline: "Fix crash in prediction", Areas: []string{"prediction"}, IsFix: true, Described: true}
	other := diffCommit{Title: "Update map data", Headline: "Update map data", Areas: []string{}, Described: true}
	vosBehavior := diffCommit{Title: vehicleOSTitle, IsVehicleOS: true, Areas: []string{"behavior"}}
	vosOther := diffCommit{Title: vehicleOSTitle, IsVehicleOS: true, Areas: []string{}}
	cats, otherCount, otherAutomated := categorize([]diffCommit{a, fix, other, vosBehavior, vosOther})
	wantItems := map[string]int{"hmi": 1, "behavior": 1, "planner": 1, "prediction": 1, "fixes": 1}
	for _, c := range cats {
		if len(c.Items) != wantItems[c.Key] {
			t.Errorf("category %s has %d items, want %d", c.Key, len(c.Items), wantItems[c.Key])
		}
		if c.Key == "behavior" && c.Undescribed != 1 {
			t.Errorf("behavior undescribed = %d, want 1 (the Vehicle OS sync)", c.Undescribed)
		}
	}
	if len(cats) != 5 || cats[0].Key != "hmi" || cats[4].Key != "fixes" {
		t.Errorf("unexpected categories: %+v", cats)
	}
	if otherCount != 2 || otherAutomated != 1 {
		t.Errorf("other = %d (automated %d), want 2 (1)", otherCount, otherAutomated)
	}
}

func TestHumanTitle(t *testing.T) {
	cases := []struct {
		in, want string
		tags     []string
	}{
		{"FRONTIER-34904: [Trucking][CMAS] Replace lane-topology tracking regions with NEAR/FAR-field sensor ROI (#120052)",
			"Replace lane-topology tracking regions with NEAR/FAR-field sensor ROI", []string{"Trucking", "CMAS"}},
		{"FRONTIER-33963: Update usa_zone_10 HD map data (#120437)", "Update usa_zone_10 HD map data", nil},
		{"fix typo in readme", "Fix typo in readme", nil},
		{"Vehicle OS Change", "Vehicle OS Change", nil},
		{"FRONTIER-1: (#5)", "", nil},
		{"FRONTIER-34975: Revert \"FRONTIER-34267: [Sim] Re-enable CL Planner-only LogSim\" (#120503)",
			"Reverted: Re-enable CL Planner-only LogSim", []string{"Sim"}},
	}
	for _, c := range cases {
		got, tags := humanTitle(c.in)
		if got != c.want || !reflect.DeepEqual(tags, c.tags) {
			t.Errorf("humanTitle(%q) = (%q, %v), want (%q, %v)", c.in, got, tags, c.want, c.tags)
		}
	}
}

func TestHasDescription(t *testing.T) {
	mk := func(title string, vos bool) diffCommit {
		c := diffCommit{Title: title, IsVehicleOS: vos}
		c.Headline, c.Tags = humanTitle(title)
		return c
	}
	if hasDescription(mk("Vehicle OS Change", true)) {
		t.Error("Vehicle OS sync must not count as described")
	}
	if hasDescription(mk("FRONTIER-24447: [auto] Update tiled_maps dependency (#120003)", false)) {
		t.Error("[auto] dependency bump must not count as described")
	}
	if hasDescription(mk("Bump requirements lockfile (#1)", false)) {
		t.Error("lockfile bump must not count as described")
	}
	if !hasDescription(mk("FRONTIER-35029: [Trucking] Bump composite-vehicle handwheel normalization to 925 deg (#121071)", false)) {
		t.Error("a real change that happens to say 'bump' must count as described")
	}
	if !hasDescription(mk("FRONTIER-34875: Mark run duration using dashed lines in HIL expt trace report (#119894)", false)) {
		t.Error("normal PR must count as described")
	}
}

func TestClassifyImpact(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{[]string{"onroad/hmi/driver/screen.cc", "trucking/offboard/report.py"}, impactVisible}, // any visible file wins
		{[]string{"onroad/behavior/planning/lane_change.cc"}, impactDriving},
		{[]string{"trucking/mapping/usa_zone_10.textpb", "common/adp_map_tiles/tiles.json"}, impactDriving},
		{[]string{"onroad/perception/lidar/cluster.cc", "common/localization/ekf.cc"}, impactInternal},
		{[]string{"vehicle_os/middleware/bus.cc"}, impactInternal},
		{[]string{"trucking/simulation/scenarios/merge.yaml", "trucking/dashboards/sys.json"}, impactOff},
		{[]string{"onroad/behavior/planning/lane_change_test.cc"}, impactOff}, // tests only
		{[]string{"trucking/planning/BUILD", "README.md"}, impactOff},         // build glue + docs only
		{[]string{"onroad/behavior/planning/lane_change.cc", "trucking/tools/x.py"}, impactDriving},
		{nil, impactUnknown},
	}
	for _, c := range cases {
		if got := classifyImpact(c.paths); got != c.want {
			t.Errorf("classifyImpact(%v) = %q, want %q", c.paths, got, c.want)
		}
	}
}

func TestPlainTitle(t *testing.T) {
	cases := map[string]string{
		"Reuse waypoints in LaneBoundaryExcursionCost::ComputeCost":     "Reuse points along the planned path in lane-edge penalty score",
		"Bump composite-vehicle handwheel normalization to 925 deg":     "Bump composite-vehicle steering wheel scaling to 925 deg",
		"Retune cut-in prediction horizon for trucks":                   "Retune car pulling in front guessing what others will do horizon for trucks",
		"Anchor HD map to map-matched localization instead of raw GNSS": "Anchor detailed road map to map-matched knowing where the truck is instead of raw satellite positioning (GPS)",
		"Show remaining ODD distance on driver display":                 "Show remaining self-driving zone (ODD) distance on driver display",
	}
	for in, want := range cases {
		if got := plainTitle(in); got != want {
			t.Errorf("plainTitle(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestChangeKind(t *testing.T) {
	cases := map[string]string{
		"Reuse waypoints in LaneBoundaryExcursionCost::ComputeCost": kindSpeedup,
		"Reverted: Re-enable CL Planner-only LogSim":                kindRevert,
		"Fix planner stall when route has back-to-back merges":      kindFix,
		"Bump composite-vehicle handwheel normalization to 925 deg": kindTuning,
		"Yield earlier to emergency vehicles":                       kindOther,
		"Add RelocationHandler trigger handler":                     kindNew,
		"Refactor lane change state machine":                        kindRefactor,
	}
	for in, want := range cases {
		if got := changeKind(in, ""); got != want {
			t.Errorf("changeKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractExcerpt(t *testing.T) {
	body := strings.Split("## Problems\n\n1. The HD map overlay in the RA viz used raw GNSS (Applanix) to position\nitself relative to the ego vehicle. Raw GNSS can be several metres off —\nespecially in areas with poor satellite geometry — so operators would\nsee the truck sitting in the wrong lane. Even when the camera feed showed it correctly.\n- <img width=\"257\" alt=\"image\" src=\"x\"/>\n2. There was also a rotational mismatch.\n\nGitOrigin-RevId: abc", "\n")
	got := extractExcerpt(body)
	if !strings.HasPrefix(got, "1. The HD map overlay in the RA viz used raw GNSS") {
		t.Errorf("excerpt start = %q", got)
	}
	if strings.Contains(got, "GitOrigin") || strings.Contains(got, "<img") {
		t.Errorf("excerpt leaked trailers/markup: %q", got)
	}
	if strings.Count(got, ". ")+1 > 4 {
		t.Errorf("excerpt should be at most three sentences: %q", got)
	}
	if extractExcerpt([]string{"", "GitOrigin-RevId: x"}) != "" {
		t.Error("trailer-only body must give an empty excerpt")
	}

	// Link debris and ticket keys at the start must not survive (real PR #113119 shape).
	wrapped := strings.Split("FRONTIER-33611; [Parent](https://github.com/x/y/pull/113100\n)\nGetLaneBoundaryDiscomfort takes waypoints instead of trajectory and no longer\nre-samples waypoints every time the function is called.\n", "\n")
	if got := extractExcerpt(wrapped); !strings.HasPrefix(got, "GetLaneBoundaryDiscomfort takes waypoints") {
		t.Errorf("excerpt with wrapped link = %q", got)
	}
}

func TestCategorizeIgnoresOffAndFlags(t *testing.T) {
	items := []diffCommit{
		{Title: "a", Headline: "a", Described: true, Areas: []string{"planner"}, Impact: impactDriving, Excerpt: "It does X.", Kind: kindTuning},
		{Title: "b", Headline: "b", Described: true, Areas: []string{"planner"}, Impact: impactOff},
		{Title: vehicleOSTitle, IsVehicleOS: true, Areas: []string{"planner"}, Impact: impactDriving},
		{Title: vehicleOSTitle, IsVehicleOS: true, Areas: []string{"planner"}, Impact: impactInternal},
		{Title: "e", Headline: "e", Described: true, Areas: []string{}, Impact: impactOff},
		{Title: "f", Headline: "f", Described: true, Areas: []string{}, Impact: impactInternal},
	}
	for i := range items {
		finalizeImpact(&items[i])
	}
	cats, otherCount, _ := categorize(items)
	var planner diffCategory
	for _, c := range cats {
		if c.Key == "planner" {
			planner = c
		}
	}
	if len(planner.Items) != 1 || planner.Items[0].Title != "a" {
		t.Errorf("off-truck change must be ignored; items = %+v", planner.Items)
	}
	if planner.Undescribed != 2 || planner.UndescribedDriving != 1 {
		t.Errorf("undescribed = %d (driving %d), want 2 (1)", planner.Undescribed, planner.UndescribedDriving)
	}
	if len(planner.Flagged) != 1 || planner.Flagged[0].Impact != impactDriving {
		t.Errorf("flagged should hold the undescribed driving sync: %+v", planner.Flagged)
	}
	if _, ok := planner.Impact[impactOff]; ok {
		t.Error("off must not be counted in category impact")
	}
	if otherCount != 1 {
		t.Errorf("otherCount = %d, want 1 (off-truck 'e' ignored)", otherCount)
	}
	if items[0].NeedsInfo || items[0].Note == "" {
		t.Errorf("described driving change with excerpt: needs_info=%v note=%q", items[0].NeedsInfo, items[0].Note)
	}
	if !items[2].NeedsInfo {
		t.Error("undescribed driving change must be flagged")
	}
	if items[3].NeedsInfo || items[3].Note != "" {
		t.Error("internal change must not be flagged or annotated")
	}
}

func TestRefineImpact(t *testing.T) {
	if got := refineImpact(impactInternal, `Revert "FRONTIER-34267: [Sim] Re-enable CL Planner-only LogSim"`); got != impactOff {
		t.Errorf("sim-titled internal change should be off, got %q", got)
	}
	if got := refineImpact(impactDriving, "[Sim] tune planner for LogSim"); got != impactDriving {
		t.Errorf("driving verdict must be kept, got %q", got)
	}
	if got := refineImpact(impactInternal, "Improve lidar clustering"); got != impactInternal {
		t.Errorf("non-sim internal must stay internal, got %q", got)
	}
}

func TestCategorizeImpactCounts(t *testing.T) {
	items := []diffCommit{
		{Title: "a", Headline: "a", Described: true, Areas: []string{"planner"}, Impact: impactDriving},
		{Title: "b", Headline: "b", Described: true, Areas: []string{"planner"}, Impact: impactOff},
		{Title: vehicleOSTitle, IsVehicleOS: true, Areas: []string{"planner"}, Impact: impactInternal},
		{Title: "d", Headline: "d", Described: true, Areas: []string{}, Impact: impactOff},
	}
	cats, _, _ := categorize(items)
	var planner diffCategory
	for _, c := range cats {
		if c.Key == "planner" {
			planner = c
		}
	}
	// Off-truck changes are ignored inside areas; only on-truck classes are tallied.
	if planner.Impact[impactDriving] != 1 || planner.Impact[impactInternal] != 1 || planner.Impact[impactOff] != 0 {
		t.Errorf("planner impact counts = %v", planner.Impact)
	}
	all := impactCounts(items)
	if all[impactOff] != 2 || all[impactDriving] != 1 || all[impactInternal] != 1 {
		t.Errorf("overall impact counts = %v", all)
	}
}

func TestParseSimpleJSON(t *testing.T) {
	raw := "```json\n{\"en\":{\"overall\":\"A quiet night.\",\"areas\":{\"hmi\":{\"sentence\":\"Nothing changed on the screen.\",\"bullets\":[]},\"fixes\":{\"sentence\":\"One problem was fixed.\",\"bullets\":[\"The dashboard loads again.\"]}}},\"ja\":{\"overall\":\"静かな夜でした。\",\"areas\":{}}}\n```"
	got, err := parseSimpleJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["en"].Overall != "A quiet night." || got["en"].Areas["fixes"].Bullets[0] != "The dashboard loads again." {
		t.Errorf("unexpected parse result: %+v", got["en"])
	}
	if got["ja"].Overall != "静かな夜でした。" {
		t.Errorf("japanese missing: %+v", got["ja"])
	}
	if _, err := parseSimpleJSON(`{"ja":{"overall":"x","areas":{}}}`); err == nil {
		t.Error("missing English rendering should be an error")
	}
	if _, err := parseSimpleJSON(`not json`); err == nil {
		t.Error("invalid JSON should be an error")
	}
}

func TestDryRunDiff(t *testing.T) {
	d := dryRunDiff("trucking-scheduled-night-2026-08-31", "trucking-scheduled-night-2026-09-01")
	if d.BaseDate != "2026-08-31" || d.HeadDate != "2026-09-01" {
		t.Errorf("dates = %s / %s", d.BaseDate, d.HeadDate)
	}
	if len(d.Categories) != 5 {
		t.Fatalf("categories = %d, want 5", len(d.Categories))
	}
	if d.OtherCount != 2 || d.OtherAutomated != 1 {
		t.Errorf("other = %d (automated %d), want 2 (1)", d.OtherCount, d.OtherAutomated)
	}
	for _, c := range d.Categories {
		for _, it := range c.Items {
			if !it.Described || it.Headline == "" {
				t.Errorf("listed item must be described with a headline: %+v", it)
			}
		}
	}
}

func TestAreaForPath(t *testing.T) {
	cases := map[string]string{
		"onroad/behavior/prediction/model.py":        "prediction", // longest prefix beats onroad/behavior/
		"onroad/behavior/components/x.cc":            "behavior",
		"trucking/hmi/monitor/a.cc":                  "hmi",
		"trucking/interfaces/sds_planner_nodes/n.cc": "planner",
		"common/localization/x.cc":                   "",
	}
	for p, want := range cases {
		if got := areaForPath(p); got != want {
			t.Errorf("areaForPath(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestParseDiffJSON(t *testing.T) {
	if d, err := parseDiffJSON("  "); d != nil || err != nil {
		t.Errorf("empty -> (%v, %v)", d, err)
	}
	if _, err := parseDiffJSON(`{"head":"a"}`); err == nil {
		t.Error("missing base should error")
	}
	d, err := parseDiffJSON(`{"base":"a","head":"b","categories":[{"key":"hmi","label":"HMI","items":[]}]}`)
	if err != nil || d.Base != "a" || len(d.Categories) != 1 {
		t.Errorf("parse: %+v %v", d, err)
	}
}
