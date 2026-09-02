package main

import (
	"regexp"
	"strings"
)

// Plain-language rendering of engineering commit titles for non-technical
// readers: split identifiers into words, then swap jargon for everyday terms.
// Example:
//
//	"Reuse waypoints in LaneBoundaryExcursionCost::ComputeCost"
//	-> "Reuse points along the planned path in the lane-edge penalty score"

// glossaryEntry maps a jargon phrase (matched case-insensitively on word
// boundaries) to plain words. Longer phrases are applied first.
type glossaryEntry struct {
	term  string
	plain string
}

var glossary = []glossaryEntry{
	{"lane boundary excursion cost", "lane-edge penalty score"},
	{"lane boundary excursion", "drifting toward the lane edge"},
	{"lane boundary", "lane edge"},
	{"cost function", "penalty score"},
	{"compute cost", "penalty score calculation"},
	{"waypoints", "points along the planned path"},
	{"waypoint", "point along the planned path"},
	{"trajectory", "planned path"},
	{"trajectories", "planned paths"},
	{"ego vehicle", "our truck"},
	{"ego", "our truck"},
	{"cmas", "collision-avoidance system (CMAS)"},
	{"mrm", "safe-stop maneuver (MRM)"},
	{"odd", "self-driving zone (ODD)"},
	{"hd map", "detailed road map"},
	{"hd maps", "detailed road maps"},
	{"localization", "knowing where the truck is"},
	{"perception", "seeing the road"},
	{"prediction", "guessing what others will do"},
	{"cut-in", "car pulling in front"},
	{"cut-ins", "cars pulling in front"},
	{"latency", "delay"},
	{"handwheel", "steering wheel"},
	{"normalization", "scaling"},
	{"gnss", "satellite positioning (GPS)"},
	{"applanix", "satellite positioning (GPS unit)"},
	{"roi", "area of interest"},
	{"logsim", "replay simulation"},
	{"regression", "problem that came back"},
	{"deceleration", "braking"},
	{"decel", "braking"},
	{"acceleration", "speeding up"},
	{"accel", "speeding up"},
	{"throttle", "gas pedal"},
	{"parameters", "settings"},
	{"parameter", "setting"},
	{"params", "settings"},
	{"param", "setting"},
	{"config", "settings"},
	{"threshold", "limit"},
	{"thresholds", "limits"},
	{"remote assistance", "remote assistance (a person helping from the office)"},
	{"ra ", "remote assistance "},
	{"health monitor", "health monitor (the truck's self-check)"},
	{"metrics", "measurements"},
	{"metric", "measurement"},
	{"heuristic", "rule of thumb"},
	{"lidar", "laser scanner (lidar)"},
	{"radar", "radar sensor"},
	{"fusion", "combining sensor data"},
	{"occlusion", "blocked view"},
	{"occluded", "hidden from view"},
	{"yield", "let others go first"},
	{"yielding", "letting others go first"},
	{"merge", "merge (joining traffic)"},
	{"kinematics", "motion"},
	{"tick", "cycle"},
}

var glossaryRe = func() []*regexp.Regexp {
	res := make([]*regexp.Regexp, len(glossary))
	for i, g := range glossary {
		res[i] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(g.term) + `\b`)
	}
	return res
}()

var (
	camelSplitRe  = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	acronymCamel  = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	scopeSuffixRe = regexp.MustCompile(`::\w+`) // Foo::Bar -> Foo
	identCharsRe  = regexp.MustCompile(`[_/]+`)
	multiSpaceRe  = regexp.MustCompile(`\s+`)
)

// plainTitle rewrites an engineering headline in plain words.
func plainTitle(headline string) string {
	s := headline
	s = scopeSuffixRe.ReplaceAllString(s, "")
	s = acronymCamel.ReplaceAllString(s, "$1 $2")
	s = camelSplitRe.ReplaceAllString(s, "$1 $2")
	s = identCharsRe.ReplaceAllString(s, " ")
	for i, g := range glossary {
		s = glossaryRe[i].ReplaceAllString(s, g.plain)
	}
	s = strings.TrimSpace(multiSpaceRe.ReplaceAllString(s, " "))
	if s == "" {
		return headline
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// Change kinds drive the "what this means for driving" note.
const (
	kindRevert   = "revert"
	kindFix      = "fix"
	kindSpeedup  = "speedup"
	kindTuning   = "tuning"
	kindRefactor = "refactor"
	kindNew      = "new"
	kindRemoval  = "removal"
	kindOther    = "other"
)

var kindRules = []struct {
	kind string
	re   *regexp.Regexp
}{
	{kindRevert, regexp.MustCompile(`(?i)\brevert`)},
	{kindFix, regexp.MustCompile(`(?i)\b(fix|fixes|fixed|bug|crash|regression|hotfix|resolve[sd]?)\b`)},
	{kindSpeedup, regexp.MustCompile(`(?i)\b(reuse|cache|cached|caching|speed ?up|faster|latency|optimi[sz]e[sd]?|perf|performance|reduce .*(time|cost|load)|cheaper)\b`)},
	{kindRefactor, regexp.MustCompile(`(?i)\b(refactor|clean ?up|rename|renaming|move|reorgani[sz]e|dead code|typo|comments?|lint|tidy|split|extract)\b`)},
	{kindTuning, regexp.MustCompile(`(?i)\b(tune|retune|tuning|threshold|param|parameter|calibrat|adjust|bump|increase|decrease|lower|raise|relax|tighten)\b`)},
	{kindRemoval, regexp.MustCompile(`(?i)\b(disable|remove|drop|turn off|deprecate)\b`)},
	{kindNew, regexp.MustCompile(`(?i)\b(add|adds|added|enable|implement|introduce|support|new|allow)\b`)},
}

// changeKind classifies a change from its title (falling back to the excerpt).
func changeKind(title, excerpt string) string {
	for _, r := range kindRules {
		if r.re.MatchString(title) {
			return r.kind
		}
	}
	for _, r := range kindRules {
		if r.re.MatchString(excerpt) {
			return r.kind
		}
	}
	return kindOther
}

// behaviorNote is the English "what this means for the truck" sentence for a
// change that runs on the truck. The browser uses i18n keys note_<kind>
// instead; this text feeds the Confluence page and the model prompt.
func behaviorNote(kind, impact string) string {
	var s string
	switch kind {
	case kindSpeedup:
		s = "Efficiency change: the truck is meant to drive exactly the same, just with less computing delay. A driving difference here would be worth reporting."
	case kindRefactor:
		s = "Code tidy-up: no change to how the truck drives is intended."
	case kindTuning:
		s = "Settings adjusted: the truck may react a little differently (earlier or later, harder or softer) in the situations this covers."
	case kindFix:
		s = "A problem was fixed: the truck should now do the right thing where it previously misbehaved."
	case kindRevert:
		s = "An earlier change was undone: driving goes back to how it was before that change."
	case kindNew:
		s = "New behavior: expect the truck to do something it did not do before in the situations this covers."
	case kindRemoval:
		s = "Something was removed or switched off: the truck will no longer do this."
	default:
		s = "This changes how the truck drives; read the description to see how."
	}
	if impact == impactVisible {
		return "On the driver's screen or in the sounds it makes. " + s
	}
	return s
}
