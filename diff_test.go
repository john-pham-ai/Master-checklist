package main

import (
	"reflect"
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
	vos.Commit.Message = "Vehicle OS Change\n\nGitOrigin-RevId: a9bbd36c"
	if p := parseCommit(vos); !p.IsVehicleOS || p.Summary != "" || p.IsFix {
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
