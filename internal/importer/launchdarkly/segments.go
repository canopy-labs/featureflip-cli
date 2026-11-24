package launchdarkly

import (
	"encoding/json"
	"fmt"
	"sort"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// segInfo is a LaunchDarkly segment in one environment, in the two forms a
// flag rule can use: a Featureflip segment key (when it is exported), or its
// conditions in disjunctive form, to inline into the rule.
type segInfo struct {
	key      string
	exported bool
	alts     [][]im.Condition // OR of ANDs; empty means it matches no one
	err      string           // why no flag rule can use it
}

// segmentUse is what one segmentMatch clause adds to a rule.
type segmentUse struct {
	ref    *segInfo            // the rule matches this exported segment
	base   []im.Condition      // ANDed into the rule
	groups []im.ConditionGroup // extra ANDed groups
	alts   [][]im.Condition    // alternatives the rule expands over
	never  bool                // the rule can match no one
}

// segments translates every environment's segments, and exports as
// Featureflip segments those that are one set of ANDed conditions, the same
// in every environment that has them. The others are inlined into the flag
// rules that use them.
func (t *translator) segments(byEnv map[string][]segment) []im.Segment {
	type seen struct {
		first segment
		infos []*segInfo
	}
	var order []string
	all := map[string]*seen{}
	for _, e := range t.envs {
		t.segs[e.ld] = map[string]*segInfo{}
		for _, s := range byEnv[e.ld] {
			if s.Deleted {
				continue
			}
			info := t.segment(s, e)
			t.segs[e.ld][s.Key] = info
			if all[s.Key] == nil {
				all[s.Key] = &seen{first: s}
				order = append(order, s.Key)
			}
			all[s.Key].infos = append(all[s.Key].infos, info)
		}
	}

	out := []im.Segment{}
	for _, src := range order {
		sn := all[src]
		why := exportProblem(sn.infos)
		if why != "" {
			t.unexported[src] = why
			continue
		}
		key := t.segKeys.Key(src)
		if key == "" {
			t.unexported[src] = "its key has no characters a Featureflip key can use"
			continue
		}
		if key != src {
			t.rep.Add(im.Info, im.KindSegment, src, "key-renamed", fmt.Sprintf("imported with the key %q", key), "")
		}
		for _, info := range sn.infos {
			info.key, info.exported = key, true
		}
		out = append(out, im.Segment{
			Key: key, SourceKey: src, Name: firstNonEmpty(sn.first.Name, src), Description: sn.first.Description,
			Conditions: sn.infos[0].alts[0],
		})
	}
	// Two exported segments sharing a key can't both be written: inline both.
	kept := out[:0]
	for _, s := range out {
		if owners := t.segKeys.Owners(s.Key); len(owners) > 1 {
			t.unexported[s.SourceKey] = fmt.Sprintf("%s become the same key %q", quoteList(owners), s.Key)
			for _, info := range all[s.SourceKey].infos {
				info.exported = false
			}
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

// exportProblem says why a segment can't be one Featureflip segment, or "".
func exportProblem(infos []*segInfo) string {
	for _, info := range infos {
		switch {
		case info.err != "":
			return "it can't be translated"
		case len(info.alts) == 0:
			return "it matches no one"
		case len(info.alts) > 1:
			return fmt.Sprintf("it matches %d different ways, and a Featureflip segment is one set of ANDed conditions", len(info.alts))
		case len(info.alts[0]) == 0:
			return "it matches everyone"
		}
	}
	first := sigOf(infos[0].alts[0])
	for _, info := range infos[1:] {
		if sigOf(info.alts[0]) != first {
			return "it differs between environments, and a Featureflip segment is the same in all of them"
		}
	}
	return ""
}

// reportUnexported notes, once every flag is translated, each segment that
// is not created in Featureflip.
func (t *translator) reportUnexported() {
	keys := make([]string, 0, len(t.unexported))
	for k := range t.unexported {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, src := range keys {
		// Its errors already say why, and block the flags that use it.
		if t.rep.Blocked(im.KindSegment, src) {
			continue
		}
		why := t.unexported[src]
		if t.segUsed[src] {
			t.rep.Add(im.Info, im.KindSegment, src, "segment-inlined",
				fmt.Sprintf("it is not created as a Featureflip segment because %s; the flags that use it get its conditions inlined", why), "")
		} else {
			t.rep.Add(im.Warning, im.KindSegment, src, "segment-not-imported",
				fmt.Sprintf("it is not created as a Featureflip segment because %s, and no imported flag uses it", why), "")
		}
	}
}

// segment translates one environment's segment into alternatives: included
// contexts match outright, and each rule matches contexts that aren't
// excluded.
func (t *translator) segment(s segment, e envInfo) *segInfo {
	t.subjectKind, t.subjectKey = im.KindSegment, s.Key
	info := &segInfo{}
	emit := func(sev im.Severity, code, msg string) { t.rep.Add(sev, im.KindSegment, s.Key, code, msg, e.key) }
	fail := func(code, msg string) *segInfo {
		info.err = msg
		emit(im.Error, code, msg)
		return info
	}
	if s.Unbounded {
		return fail("unbounded", "it is a big segment, and LaunchDarkly's API doesn't list its members")
	}
	keyIn := func(kind string, values []string, negate bool) im.Condition {
		attr := im.UserID
		if kind != "" && kind != "user" {
			attr = kind + ".key"
		}
		t.noteKind(kind)
		return im.Condition{Attribute: attr, Operator: im.OpIn, Values: append([]string{}, values...), Negate: negate}
	}
	if len(s.Included) > 0 {
		info.alts = append(info.alts, []im.Condition{keyIn("user", s.Included, false)})
	}
	for _, ic := range s.IncludedContexts {
		if len(ic.Values) > 0 {
			info.alts = append(info.alts, []im.Condition{keyIn(ic.ContextKind, ic.Values, false)})
		}
	}
	var guards []im.Condition
	if len(s.Excluded) > 0 {
		guards = append(guards, keyIn("user", s.Excluded, true))
	}
	for _, ec := range s.ExcludedContexts {
		if len(ec.Values) > 0 {
			guards = append(guards, keyIn(ec.ContextKind, ec.Values, true))
		}
	}
	if len(s.Included)+len(s.IncludedContexts) > 0 {
		t.loose = true
	}

rules:
	for i, r := range s.Rules {
		if r.Weight != nil && *r.Weight < 100000 {
			return fail("weighted-segment-rule", fmt.Sprintf("rule %d includes only part of the contexts it matches, and Featureflip segments include all of them", i+1))
		}
		alt := []im.Condition{}
		for _, c := range r.Clauses {
			if c.Op == "segmentMatch" {
				return fail("nested-segment", fmt.Sprintf("rule %d refers to another segment, which Featureflip segments can't do", i+1))
			}
			res := t.clause(c, func(sev im.Severity, code, msg string) { emit(sev, code, fmt.Sprintf("rule %d: %s", i+1, msg)) })
			switch {
			case res.err != "":
				return fail(res.code, fmt.Sprintf("rule %d: %s", i+1, res.err))
			case res.never:
				continue rules
			case res.always:
				continue
			}
			alt = append(alt, res.conds...)
		}
		info.alts = append(info.alts, append(alt, guards...))
	}
	return info
}

// segmentClause translates a segmentMatch clause: the context is in (or,
// negated, in none of) the segments it names.
func (t *translator) segmentClause(c clause, sole bool, ld string, emit func(im.Severity, string, string)) (segmentUse, bool) {
	var keys []string
	for _, raw := range c.Values {
		var k string
		if json.Unmarshal(raw, &k) != nil || k == "" {
			emit(im.Error, "unreadable-condition", "its segment condition names a segment that isn't a key")
			return segmentUse{}, false
		}
		keys = append(keys, k)
	}
	var infos []*segInfo
	for _, k := range keys {
		t.segUsed[k] = true
		info := t.segs[ld][k]
		if info == nil {
			// LaunchDarkly matches no one against a segment the environment
			// doesn't have.
			continue
		}
		if info.err != "" {
			emit(im.Error, "segment-unusable", fmt.Sprintf("it uses the segment %q, which can't be translated (%s)", k, info.err))
			return segmentUse{}, false
		}
		infos = append(infos, info)
	}

	var alts [][]im.Condition
	for _, info := range infos {
		alts = append(alts, info.alts...)
	}
	everyone := false
	for _, a := range alts {
		if len(a) == 0 {
			everyone = true
		}
	}

	if !c.Negate {
		switch {
		case sole && len(keys) == 1 && len(infos) == 1 && infos[0].exported:
			return segmentUse{ref: infos[0]}, true
		case len(alts) == 0:
			emit(im.Info, "never-matches", "its segments match no one in this environment, so it is left out")
			return segmentUse{never: true}, true
		case everyone:
			return segmentUse{}, true
		case len(alts) == 1:
			return segmentUse{base: alts[0]}, true
		case allSingle(alts):
			g := im.ConditionGroup{Operator: im.Or}
			for _, a := range alts {
				g.Conditions = append(g.Conditions, a[0])
			}
			return segmentUse{groups: []im.ConditionGroup{g}}, true
		}
		return segmentUse{alts: alts}, true
	}

	// In none of the segments: every alternative must fail, so at least one
	// condition of each fails.
	if everyone {
		emit(im.Info, "never-matches", "it requires being outside a segment that matches everyone, so it is left out")
		return segmentUse{never: true}, true
	}
	var use segmentUse
	for _, a := range alts {
		g := im.ConditionGroup{Operator: im.Or}
		for _, cond := range a {
			g.Conditions = append(g.Conditions, cond.Negated())
		}
		use.groups = append(use.groups, g)
	}
	return use, true
}

func allSingle(alts [][]im.Condition) bool {
	for _, a := range alts {
		if len(a) != 1 {
			return false
		}
	}
	return true
}

// sigOf is a canonical form of ANDed conditions, so the same segment in two
// environments compares equal whatever the order of its conditions.
func sigOf(cs []im.Condition) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		c.Values = append([]string{}, c.Values...)
		sort.Strings(c.Values)
		b, _ := json.Marshal(c)
		parts = append(parts, string(b))
	}
	sort.Strings(parts)
	b, _ := json.Marshal(parts)
	return string(b)
}
