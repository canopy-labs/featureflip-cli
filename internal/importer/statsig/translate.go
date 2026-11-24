package statsig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// snapshot is everything read from one Statsig project. Translation is a pure
// function of it, so tests drive it from recorded API responses.
type snapshot struct {
	Environments []environment         `json:"environments"`
	Gates        []gate                `json:"gates"`
	Overrides    map[string]*overrides `json:"overrides"` // by gate id
	Configs      []dynamicConfig       `json:"dynamicConfigs"`
	Segments     []segment             `json:"segments"`
	IDLists      map[string][]string   `json:"idLists"` // by segment id
	// OverridesUnread is set when every gate's overrides answered 404.
	OverridesUnread bool `json:"overridesUnread"`
}

// maxExpandedRules caps how many Featureflip rules one Statsig rule may become
// when the segments it names have to be inlined.
const maxExpandedRules = 16

type envInfo struct {
	key  string // Featureflip environment key
	name string // Statsig environment name
	id   string
}

type translator struct {
	rep      *im.Report
	envs     []envInfo
	flagKeys *im.Keyer
	segKeys  *im.Keyer
	segs     map[string]*segInfo
}

// segInfo is a Statsig segment in the two forms a flag rule can use: a
// Featureflip segment key (when it translates to one), or its conditions in
// disjunctive form, to inline into the rule.
type segInfo struct {
	key        string
	exportable bool
	alts       [][]im.Condition // OR of ANDs; empty means it matches no one
	err        string           // why no flag rule can use it
	// warnings are the segment's own translation warnings. Every flag rule
	// that uses the segment inherits them, so they are repeated there.
	warnings []segWarning
}

type segWarning struct{ code, msg string }

// translate turns a snapshot into a Featureflip project and its report.
func translate(snap *snapshot, opts im.Options) (*im.Result, error) {
	t := &translator{
		rep:      im.NewReport(),
		flagKeys: im.NewKeyer(),
		segKeys:  im.NewKeyer(),
		segs:     map[string]*segInfo{},
	}
	project := &im.Project{Segments: []im.Segment{}, Flags: []im.Flag{}}
	if err := t.environments(snap.Environments, opts.EnvMap, project); err != nil {
		return nil, err
	}

	for _, s := range snap.Segments {
		if seg := t.segment(s, snap.IDLists[s.ID]); seg != nil {
			project.Segments = append(project.Segments, *seg)
		}
	}
	// Before any flag uses a segment: a rule must not point at a key that
	// two segments share.
	t.segmentCollisions(project)
	for _, g := range snap.Gates {
		if f := t.gate(g, snap.Overrides[g.ID]); f != nil {
			project.Flags = append(project.Flags, *f)
		}
	}
	for _, c := range snap.Configs {
		if f := t.config(c); f != nil {
			project.Flags = append(project.Flags, *f)
		}
	}
	t.collisions(project)

	if snap.OverridesUnread {
		t.rep.Add(im.Warning, im.KindProject, "", "overrides-unread",
			"Statsig answered 404 for every gate's ID overrides. If your gates have overrides, they were not read, so check them in Statsig before you switch.", "")
	}

	t.rep.Add(im.Info, im.KindProject, "", "not-read",
		"Experiments, layers, holdouts and autotunes are not read. Only feature gates, dynamic configs and segments are imported.", "")
	t.rep.Add(im.Info, im.KindProject, "", "rebucketing",
		"Rollouts keep their percentage but not their membership: Featureflip buckets users with its own hash, so a 30% rollout reaches a different 30%.", "")
	return &im.Result{Source: "Statsig", Project: project, Report: t.rep}, nil
}

func (t *translator) environments(envs []environment, envMap map[string]string, project *im.Project) error {
	if len(envs) == 0 {
		return fmt.Errorf("the Statsig project has no environments")
	}
	known := map[string]bool{}
	byKey := map[string]string{}
	for _, e := range envs {
		known[e.Name] = true
		key := im.NormalizeKey(e.Name)
		if mapped, ok := envMap[e.Name]; ok {
			key = mapped
		}
		if key == "" {
			return im.OptionError{Msg: fmt.Sprintf("the Statsig environment %q has no usable key: map it with --env-map %s=<key>", e.Name, e.Name)}
		}
		if other, dup := byKey[key]; dup {
			return im.OptionError{Msg: fmt.Sprintf("the Statsig environments %q and %q both map to %q", other, e.Name, key)}
		}
		byKey[key] = e.Name
		t.envs = append(t.envs, envInfo{key: key, name: e.Name, id: e.ID})
		project.Environments = append(project.Environments, im.Environment{Key: key, Source: e.Name})
	}
	var unknown []string
	for name := range envMap {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		names := make([]string, 0, len(envs))
		for _, e := range envs {
			names = append(names, e.Name)
		}
		return im.OptionError{Msg: fmt.Sprintf("--env-map names %s, which the Statsig project doesn't have (it has %s)",
			strings.Join(unknown, ", "), strings.Join(names, ", "))}
	}
	return nil
}

// collisions blocks every flag whose key another flag normalized to as well.
func (t *translator) collisions(p *im.Project) {
	for _, f := range p.Flags {
		if owners := t.flagKeys.Owners(f.Key); len(owners) > 1 {
			t.rep.Add(im.Error, im.KindFlag, f.SourceKey, "key-collision",
				fmt.Sprintf("%s become the same key %q; rename all but one in Statsig first", quoteList(owners), f.Key), "")
		}
	}
}

// segmentCollisions blocks every segment whose key another segment
// normalized to as well, and makes it unusable to flag rules.
func (t *translator) segmentCollisions(p *im.Project) {
	for _, s := range p.Segments {
		if owners := t.segKeys.Owners(s.Key); len(owners) > 1 {
			msg := fmt.Sprintf("%s become the same key %q; rename all but one in Statsig first", quoteList(owners), s.Key)
			t.rep.Add(im.Error, im.KindSegment, s.SourceKey, "key-collision", msg, "")
			if info := t.segs[s.SourceKey]; info != nil && info.err == "" {
				info.err = msg
			}
		}
	}
}

// ---------------------------------------------------------------- segments

func (t *translator) segment(s segment, ids []string) *im.Segment {
	key := t.segKeys.Key(s.ID)
	info := &segInfo{key: key}
	t.segs[s.ID] = info
	fail := func(code, msg string) {
		info.err = msg
		t.rep.Add(im.Error, im.KindSegment, s.ID, code, msg, "")
	}
	out := &im.Segment{Key: key, SourceKey: s.ID, Name: firstNonEmpty(s.Name, s.ID), Description: s.Description}
	if key == "" {
		fail("key-unusable", "its id has no characters a Featureflip key can use")
		return out
	}
	if key != s.ID {
		t.rep.Add(im.Info, im.KindSegment, s.ID, "key-renamed", fmt.Sprintf("imported with the key %q", key), "")
	}
	if !s.IsEnabled {
		fail("segment-disabled", "the segment is disabled in Statsig, and Featureflip segments have no disabled state")
		return out
	}

	switch s.Type {
	case "id_list":
		attr := unitAttribute(s.IDType)
		members := append([]string{}, ids...)
		cond := im.Condition{Attribute: attr, Operator: im.OpIn, Values: members}
		if len(members) == 0 {
			info.alts = nil // an empty list matches no one
		} else {
			info.alts = [][]im.Condition{{cond}}
		}
		info.exportable = true
		out.Conditions = []im.Condition{cond}
		if attr != im.UserID {
			t.rep.Add(im.Info, im.KindSegment, s.ID, "unit-attribute",
				fmt.Sprintf("its IDs are %s values, read from the context attribute %q", s.IDType, attr), "")
		}
		t.rep.Add(im.Info, im.KindSegment, s.ID, "id-case",
			"Featureflip matches its IDs case-insensitively, and Statsig matches them exactly", "")
	case "rule_based":
		for _, r := range s.Rules {
			if r.PassPercentage < 100 {
				fail("segment-partial-rule", fmt.Sprintf("rule %q includes only %s%% of matching users; Featureflip segments include all of them", r.Name, pct(r.PassPercentage)))
				return out
			}
			if len(r.Environments) > 0 && !t.coversAll(r.Environments) {
				fail("segment-env-scoped", fmt.Sprintf("rule %q applies only in %s, and a Featureflip segment is the same in every environment", r.Name, strings.Join(r.Environments, ", ")))
				return out
			}
			var alt []im.Condition
			for _, c := range r.Conditions {
				emit := func(sev im.Severity, code, msg string) {
					msg = fmt.Sprintf("rule %q: %s", r.Name, msg)
					t.rep.Add(sev, im.KindSegment, s.ID, code, msg, "")
					if sev == im.Warning {
						info.warnings = append(info.warnings, segWarning{code, msg})
					}
				}
				res := simpleCondition(c, s.IDType, nil, emit)
				switch {
				case res.err != "":
					fail(res.code, fmt.Sprintf("rule %q: %s", r.Name, res.err))
					return out
				case res.segment != nil:
					fail("nested-segment", fmt.Sprintf("rule %q refers to another segment, which Featureflip segments can't do", r.Name))
					return out
				case res.always:
				default:
					alt = append(alt, res.cond)
				}
			}
			info.alts = append(info.alts, alt)
		}
		if len(info.alts) == 1 {
			info.exportable = true
			out.Conditions = info.alts[0]
		} else {
			msg := fmt.Sprintf("it has %d rules, and a Featureflip segment is one set of ANDed conditions. It is not created as a segment; flags that use it get its rules inlined", len(info.alts))
			if len(info.alts) == 0 {
				msg = "it has no rules, so it matches no one. It is not created as a segment"
			}
			t.rep.Add(im.Error, im.KindSegment, s.ID, "segment-not-representable", msg, "")
		}
	default:
		fail("segment-type", fmt.Sprintf("its type %q can't be read through the Console API", s.Type))
		return out
	}
	if out.Conditions == nil {
		out.Conditions = []im.Condition{}
	}
	return out
}

// ---------------------------------------------------------------- flags

// flagCtx is what translating one gate's or config's rules needs to know.
type flagCtx struct {
	source     string
	idType     string
	bucketBy   string
	pass       func(r rule) string // the variation a passing unit gets
	fail       string              // the variation a failing unit gets
	variations int
}

func (t *translator) newFlag(id, name, desc string, tags []string, typ im.FlagType) *im.Flag {
	key := t.flagKeys.Key(id)
	f := &im.Flag{
		Key: key, SourceKey: id, Name: firstNonEmpty(name, id), Description: desc,
		Type: typ, Tags: tags, Environments: map[string]*im.EnvConfig{},
	}
	switch {
	case key == "":
		t.rep.Add(im.Error, im.KindFlag, id, "key-unusable", "its id has no characters a Featureflip key can use", "")
	case key != id:
		t.rep.Add(im.Warning, im.KindFlag, id, "key-renamed",
			fmt.Sprintf("imported with the key %q, so the code that reads %q must use the new key", key, id), "")
	}
	return f
}

func (t *translator) common(f *im.Flag, idType string, targetApps, holdouts []string) {
	if len(targetApps) > 0 {
		t.rep.Add(im.Warning, im.KindFlag, f.SourceKey, "target-apps",
			fmt.Sprintf("Statsig evaluates it only in the target apps %s; Featureflip serves it to every app using the environment's SDK key", strings.Join(targetApps, ", ")), "")
	}
	if len(holdouts) > 0 {
		t.rep.Add(im.Warning, im.KindFlag, f.SourceKey, "holdouts",
			"it is in a Statsig holdout, which is not imported: held-out users will get the flag", "")
	}
	if attr := unitAttribute(idType); attr != im.UserID {
		t.rep.Add(im.Info, im.KindFlag, f.SourceKey, "unit-attribute",
			fmt.Sprintf("its unit type is %s, so rollouts bucket by the context attribute %q", idType, attr), "")
	}
}

func (t *translator) gate(g gate, o *overrides) *im.Flag {
	if g.IsTemplate || g.Type == "TEMPLATE" {
		t.rep.Add(im.Info, im.KindProject, "", "template", fmt.Sprintf("Skipped the gate template %q.", g.ID), "")
		return nil
	}
	if g.Status == "Archived" {
		t.rep.Add(im.Info, im.KindProject, "", "archived", fmt.Sprintf("Skipped the archived gate %q.", g.ID), "")
		return nil
	}
	f := t.newFlag(g.ID, g.Name, g.Description, g.Tags, im.Boolean)
	f.Variations = []im.Variation{
		{Key: "true", Name: "True", Value: "true"},
		{Key: "false", Name: "False", Value: "false"},
	}
	t.common(f, g.IDType, g.TargetApps, g.HoldoutIDs)
	ctx := flagCtx{
		source: g.ID, idType: g.IDType, bucketBy: unitAttribute(g.IDType),
		pass: func(rule) string { return "true" }, fail: "false", variations: 2,
	}

	if g.Status == "Launched" {
		// The Console API doesn't say what a launched gate serves: its rules
		// and isEnabled describe the gate before the launch decision.
		t.rep.Add(im.Error, im.KindFlag, g.ID, "launched",
			"Statsig marks it Launched, and the Console API doesn't say what a launched gate serves. Recreate it by hand, or remove it from your code if the launch is done", "")
		return f
	}

	enabled := g.IsEnabled && g.Status != "Disabled"
	for _, e := range t.envs {
		rules := t.overrideRules(g, o, e)
		for _, r := range g.Rules {
			rules = append(rules, t.rule(ctx, r, e)...)
		}
		t.unbucketableWarnings(ctx, rules, e)
		f.Environments[e.key] = &im.EnvConfig{Enabled: enabled, Rules: rules, Default: "false"}
	}
	return f
}

// unbucketableWarnings flags rollout rules with rules after them. A context
// without the bucket-by attribute skips a Featureflip rollout and reaches the
// later rules, while Statsig stops at the rule that matched.
func (t *translator) unbucketableWarnings(ctx flagCtx, rules []im.Rule, e envInfo) {
	for i, r := range rules {
		if r.Rollout != nil && i < len(rules)-1 {
			t.rep.Add(im.Warning, im.KindFlag, ctx.source, "unbucketable-falls-through",
				fmt.Sprintf("rule %q: a context without %q skips this rollout and reaches the rules after it; Statsig would stop here", r.Description, r.BucketBy), e.key)
		}
	}
}

// coversAll reports whether a rule's environment list names every environment.
func (t *translator) coversAll(envs []string) bool {
	for _, e := range t.envs {
		if !appliesTo(envs, e) {
			return false
		}
	}
	return true
}

// overrideRules turns a gate's ID overrides into leading fixed rules.
func (t *translator) overrideRules(g gate, o *overrides, e envInfo) []im.Rule {
	if o == nil {
		return []im.Rule{}
	}
	passing, failing := map[string][]string{}, map[string][]string{}
	addAll := func(m map[string][]string, attr string, ids []string) {
		for _, id := range ids {
			if !slices.Contains(m[attr], id) {
				m[attr] = append(m[attr], id)
			}
		}
	}
	custom := unitAttribute(g.IDType)
	addAll(passing, im.UserID, o.PassingUserIDs)
	addAll(failing, im.UserID, o.FailingUserIDs)
	addAll(passing, custom, o.PassingCustomIDs)
	addAll(failing, custom, o.FailingCustomIDs)
	for _, eo := range o.EnvironmentOverrides {
		if eo.Environment != nil && !appliesTo([]string{*eo.Environment}, e) {
			continue
		}
		attr := unitAttribute(eo.UnitID)
		addAll(passing, attr, eo.PassingIDs)
		addAll(failing, attr, eo.FailingIDs)
	}

	rules := []im.Rule{}
	for _, side := range []struct {
		ids       map[string][]string
		variation string
		label     string
	}{{passing, "true", "passing"}, {failing, "false", "failing"}} {
		attrs := make([]string, 0, len(side.ids))
		for a := range side.ids {
			attrs = append(attrs, a)
		}
		sort.Strings(attrs)
		for _, a := range attrs {
			rules = append(rules, im.Rule{
				Description: fmt.Sprintf("Statsig override: %s %s", side.label, a),
				Groups:      []im.ConditionGroup{{Operator: im.And, Conditions: []im.Condition{{Attribute: a, Operator: im.OpIn, Values: side.ids[a]}}}},
				Variation:   side.variation,
			})
		}
	}
	for a, ids := range passing {
		for _, id := range ids {
			if slices.Contains(failing[a], id) {
				t.rep.Add(im.Warning, im.KindFlag, g.ID, "override-conflict",
					fmt.Sprintf("%s %q is in both the passing and the failing overrides; it is imported as passing", a, id), e.key)
			}
		}
	}
	if len(rules) > 0 {
		t.rep.Add(im.Info, im.KindFlag, g.ID, "id-case",
			"its overrides match IDs case-insensitively in Featureflip, and exactly in Statsig", "")
	}
	return rules
}

func (t *translator) config(c dynamicConfig) *im.Flag {
	if c.IsTemplate || c.Type == "TEMPLATE" {
		t.rep.Add(im.Info, im.KindProject, "", "template", fmt.Sprintf("Skipped the dynamic config template %q.", c.ID), "")
		return nil
	}
	if c.Status == "Archived" {
		t.rep.Add(im.Info, im.KindProject, "", "archived", fmt.Sprintf("Skipped the archived dynamic config %q.", c.ID), "")
		return nil
	}
	f := t.newFlag(c.ID, c.Name, c.Description, c.Tags, im.JSON)
	t.common(f, c.IDType, c.TargetApps, c.HoldoutIDs)
	if c.Schema != nil && *c.Schema != "" {
		t.rep.Add(im.Info, im.KindFlag, c.ID, "schema", "its JSON schema is not imported; Featureflip doesn't validate JSON values against a schema", "")
	}

	defaultValue, err := canonicalJSON(c.DefaultValue)
	if err != nil {
		t.rep.Add(im.Error, im.KindFlag, c.ID, "unreadable-value", "its default value is not valid JSON", "")
		return f
	}
	f.Variations = []im.Variation{{Key: "default", Name: "Default", Value: defaultValue}}
	byValue := map[string]string{defaultValue: "default"}
	usedKeys := map[string]bool{"default": true}
	for i, r := range c.Rules {
		v, err := canonicalJSON(r.ReturnValue)
		if err != nil {
			t.rep.Add(im.Error, im.KindFlag, c.ID, "unreadable-value", fmt.Sprintf("rule %q returns a value that is not valid JSON", r.Name), "")
			return f
		}
		if _, ok := byValue[v]; ok {
			continue
		}
		key := im.NormalizeKey(r.Name)
		for n := i + 1; key == "" || usedKeys[key]; n++ {
			key = fmt.Sprintf("value-%d", n)
		}
		usedKeys[key] = true
		byValue[v] = key
		f.Variations = append(f.Variations, im.Variation{Key: key, Name: firstNonEmpty(r.Name, key), Value: v})
	}

	ctx := flagCtx{
		source: c.ID, idType: c.IDType, bucketBy: unitAttribute(c.IDType),
		pass: func(r rule) string {
			v, _ := canonicalJSON(r.ReturnValue)
			return byValue[v]
		},
		fail: "default", variations: len(f.Variations),
	}
	enabled := c.IsEnabled && c.Status != "Disabled"
	for _, e := range t.envs {
		rules := []im.Rule{}
		for _, r := range c.Rules {
			rules = append(rules, t.rule(ctx, r, e)...)
		}
		t.unbucketableWarnings(ctx, rules, e)
		f.Environments[e.key] = &im.EnvConfig{Enabled: enabled, Rules: rules, Default: "default"}
	}
	return f
}

// ---------------------------------------------------------------- rules

// rule translates one Statsig rule for one environment. A Statsig rule whose
// conditions match is terminal: units outside its pass percentage get the
// fail value and evaluation stops. A Featureflip rollout rule behaves the same
// way when the flag has two variations, which is why a partial rollout of a
// config with more values is refused.
func (t *translator) rule(ctx flagCtx, r rule, e envInfo) []im.Rule {
	if len(r.Environments) > 0 && !appliesTo(r.Environments, e) {
		return nil
	}
	emit := func(sev im.Severity, code, msg string) {
		t.rep.Add(sev, im.KindFlag, ctx.source, code, fmt.Sprintf("rule %q: %s", r.Name, msg), e.key)
	}
	if len(r.Variants) > 0 {
		emit(im.Error, "variants", "it splits passing users across value variants, which a Featureflip rule can't do")
		return nil
	}

	var base []im.Condition
	var groups []im.ConditionGroup
	var disjunctions [][][]im.Condition
	var segRefs []*segInfo // segments the rule's only condition names, for a segment rule
	soleSegment := len(r.Conditions) == 1

	for _, c := range r.Conditions {
		res := simpleCondition(c, ctx.idType, &e, emit)
		switch {
		case res.err != "":
			emit(im.Error, res.code, res.err)
			return nil
		case res.never:
			emit(im.Info, "never-matches", "its environment condition excludes this environment, so it is left out there")
			return nil
		case res.always:
			continue
		case res.segment != nil:
			infos := make([]*segInfo, 0, len(res.segment.ids))
			for _, id := range res.segment.ids {
				info, ok := t.segs[id]
				if !ok {
					emit(im.Error, "unknown-segment", fmt.Sprintf("it names the segment %q, which the Console API didn't return", id))
					return nil
				}
				if info.err != "" {
					emit(im.Error, "segment-unusable", fmt.Sprintf("it uses the segment %q, which can't be translated (%s)", id, info.err))
					return nil
				}
				for _, w := range info.warnings {
					emit(im.Warning, w.code, fmt.Sprintf("its segment %q, %s", id, w.msg))
				}
				infos = append(infos, info)
			}
			if !res.segment.negate {
				if soleSegment && len(infos) == 1 && infos[0].exportable {
					segRefs = infos
					continue
				}
				var alts [][]im.Condition
				for _, info := range infos {
					alts = append(alts, info.alts...)
				}
				if len(alts) == 0 {
					emit(im.Info, "never-matches", "its segments match no one, so it is left out")
					return nil
				}
				if len(alts) == 1 {
					base = append(base, alts[0]...)
				} else if allSingle(alts) {
					g := im.ConditionGroup{Operator: im.Or}
					for _, a := range alts {
						g.Conditions = append(g.Conditions, a[0])
					}
					groups = append(groups, g)
				} else {
					disjunctions = append(disjunctions, alts)
				}
				continue
			}
			// Failing every segment: for each segment rule, at least one of
			// its conditions must fail.
			for _, info := range infos {
				for _, alt := range info.alts {
					if len(alt) == 0 {
						emit(im.Info, "never-matches", "it requires failing a segment that matches everyone, so it is left out")
						return nil
					}
					g := im.ConditionGroup{Operator: im.Or}
					for _, c := range alt {
						g.Conditions = append(g.Conditions, c.Negated())
					}
					groups = append(groups, g)
				}
			}
		default:
			base = append(base, res.cond)
		}
	}

	serve, ok := t.serve(ctx, r, emit)
	if !ok {
		return nil
	}
	if len(r.PendingAutomatedRollouts) > 0 {
		emit(im.Warning, "scheduled-rollout", fmt.Sprintf("its scheduled rollout steps (%d) are not imported; it is imported at its current %s%%", len(r.PendingAutomatedRollouts), pct(r.PassPercentage)))
	}

	if len(segRefs) == 1 {
		out := serve
		out.SegmentKey = segRefs[0].key
		out.Groups = []im.ConditionGroup{}
		return []im.Rule{out}
	}

	combos := [][]im.Condition{nil}
	for _, alts := range disjunctions {
		var next [][]im.Condition
		for _, prefix := range combos {
			for _, alt := range alts {
				next = append(next, append(append([]im.Condition{}, prefix...), alt...))
			}
		}
		combos = next
		if len(combos) > maxExpandedRules {
			emit(im.Error, "too-many-rules", fmt.Sprintf("inlining its segments would take more than %d Featureflip rules", maxExpandedRules))
			return nil
		}
	}
	if len(combos) > 1 {
		emit(im.Info, "segment-inlined", fmt.Sprintf("its segments are inlined as %d Featureflip rules", len(combos)))
	}

	out := make([]im.Rule, 0, len(combos))
	for _, extra := range combos {
		rl := serve
		rl.Groups = []im.ConditionGroup{}
		if and := append(append([]im.Condition{}, base...), extra...); len(and) > 0 {
			rl.Groups = append(rl.Groups, im.ConditionGroup{Operator: im.And, Conditions: and})
		}
		rl.Groups = append(rl.Groups, groups...)
		out = append(out, rl)
	}
	return out
}

// serve sets what a matching rule serves.
func (t *translator) serve(ctx flagCtx, r rule, emit func(im.Severity, string, string)) (im.Rule, bool) {
	out := im.Rule{Description: r.Name}
	passVariation := ctx.pass(r)
	switch {
	case r.PassPercentage >= 100:
		out.Variation = passVariation
	case r.PassPercentage <= 0:
		out.Variation = ctx.fail
	case passVariation == ctx.fail:
		out.Variation = ctx.fail // every unit gets the same value either way
	default:
		if ctx.variations > 2 {
			emit(im.Error, "partial-rollout-multi-value",
				fmt.Sprintf("it rolls out to %s%%, and the config has %d distinct values: a Featureflip rollout would split the remaining users across all the other values instead of giving them the default", pct(r.PassPercentage), ctx.variations))
			return out, false
		}
		whole := int(math.Round(r.PassPercentage))
		whole = max(1, min(99, whole))
		if float64(whole) != r.PassPercentage {
			emit(im.Warning, "rollout-rounded", fmt.Sprintf("its %s%% rollout is imported as %d%%; Featureflip rollouts are whole percentages", pct(r.PassPercentage), whole))
		}
		out.Variation = passVariation
		out.Rollout = &whole
		out.BucketBy = ctx.bucketBy
	}
	return out, true
}

func appliesTo(envs []string, e envInfo) bool {
	for _, name := range envs {
		if strings.EqualFold(name, e.name) || (e.id != "" && name == e.id) {
			return true
		}
	}
	return false
}

func allSingle(alts [][]im.Condition) bool {
	for _, a := range alts {
		if len(a) != 1 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- conditions

type segmentRef struct {
	ids    []string
	negate bool // fails_segment: the unit must be in none of them
}

type condResult struct {
	cond    im.Condition
	always  bool // the condition holds for everyone (drop it)
	never   bool // the condition holds for no one in this environment
	segment *segmentRef
	code    string
	err     string
}

// derived names the Statsig condition types whose value Statsig fills in for
// the caller, and from what.
var derived = map[string]string{
	"country":         "the request's IP address",
	"os_name":         "the user agent",
	"os_version":      "the user agent",
	"browser_name":    "the user agent",
	"browser_version": "the user agent",
	"device_model":    "the user agent",
}

var userFields = map[string]string{
	"email":           "email",
	"country":         "country",
	"locale":          "locale",
	"app_version":     "appVersion",
	"os_name":         "osName",
	"os_version":      "osVersion",
	"browser_name":    "browserName",
	"browser_version": "browserVersion",
	"device_model":    "deviceModel",
}

// negatedOperators are Statsig's "not any" operators. They become the positive
// operator with negate set, never NotIn/NotContains: a context missing the
// attribute passes "none" in Statsig, and only a negated condition passes in
// Featureflip when the attribute is missing.
var negatedOperators = map[string]bool{"none": true, "none_case_sensitive": true, "str_contains_none": true}

var operators = map[string]im.Operator{
	"any":                 im.OpIn,
	"none":                im.OpIn, // negated below
	"any_case_sensitive":  im.OpIn,
	"none_case_sensitive": im.OpIn, // negated below
	"str_contains_any":    im.OpContains,
	"str_contains_none":   im.OpContains, // negated below
	"gt":                  im.OpGreaterThan,
	"gte":                 im.OpGreaterThanOrEqual,
	"lt":                  im.OpLessThan,
	"lte":                 im.OpLessThanOrEqual,
	"version_gt":          im.OpSemverGreater,
	"version_gte":         im.OpSemverGreaterEq,
	"version_lt":          im.OpSemverLess,
	"version_lte":         im.OpSemverLessEq,
	"version_eq":          im.OpSemverEquals,
	"str_matches":         im.OpMatchesRegex,
	"before":              im.OpBefore,
	"after":               im.OpAfter,
}

var strictSemver = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+].*)?$`)

// simpleCondition translates one Statsig condition. env is nil inside a
// segment, where environment conditions are refused.
func simpleCondition(c condition, idType string, env *envInfo, emit func(im.Severity, string, string)) condResult {
	unsupported := func(what string) condResult {
		return condResult{code: "unsupported-condition", err: what + ", which has no Featureflip equivalent"}
	}
	switch c.Type {
	case "public":
		return condResult{always: true}
	case "passes_segment", "fails_segment":
		ids, err := targetValues(c.TargetValue)
		if err != nil || len(ids) == 0 {
			return condResult{code: "unreadable-condition", err: fmt.Sprintf("its %s condition names no segment", c.Type)}
		}
		negate := c.Type == "fails_segment"
		if c.Operator == "none" {
			negate = !negate
		}
		return condResult{segment: &segmentRef{ids: ids, negate: negate}}
	case "environment_tier":
		if env == nil {
			return condResult{code: "unsupported-condition", err: "it checks the environment tier, which a Featureflip segment can't do"}
		}
		tiers, _ := targetValues(c.TargetValue)
		in := slices.ContainsFunc(tiers, func(t string) bool { return strings.EqualFold(t, env.name) })
		switch c.Operator {
		case "any", "any_case_sensitive":
		case "none", "none_case_sensitive":
			in = !in
		default:
			return unsupported(fmt.Sprintf("it checks the environment tier with %q", c.Operator))
		}
		if in {
			return condResult{always: true}
		}
		return condResult{never: true}
	case "passes_gate", "fails_gate":
		return unsupported("it checks another gate (" + c.Type + ")")
	case "time", "ip_address", "user_agent", "url", "javascript", "target_app", "experiment_group":
		return unsupported("it uses the condition type " + c.Type)
	}

	var attr string
	switch c.Type {
	case "user_id":
		attr = im.UserID
	case "custom_field":
		if c.Field == nil || *c.Field == "" {
			return condResult{code: "unreadable-condition", err: "its custom field condition names no field"}
		}
		attr = *c.Field
	case "unit_id":
		if c.CustomID == nil || *c.CustomID == "" {
			attr = unitAttribute(idType)
		} else {
			attr = unitAttribute(*c.CustomID)
		}
	default:
		field, ok := userFields[c.Type]
		if !ok {
			return unsupported("it uses the condition type " + c.Type)
		}
		attr = field
	}
	if from, ok := derived[c.Type]; ok {
		emit(im.Warning, "derived-attribute",
			fmt.Sprintf("Statsig fills %q from %s when the app doesn't send it; Featureflip reads only what your app puts in the context", attr, from))
	}

	op, ok := operators[c.Operator]
	if !ok {
		return unsupported(fmt.Sprintf("it compares %q with the operator %s", attr, c.Operator))
	}
	values, err := targetValues(c.TargetValue)
	if err != nil {
		return condResult{code: "unreadable-condition", err: fmt.Sprintf("its %q condition has an unreadable value", attr)}
	}
	if values == nil {
		values = []string{}
	}

	switch c.Operator {
	case "any_case_sensitive", "none_case_sensitive":
		emit(im.Warning, "case-insensitive",
			fmt.Sprintf("its %q comparison is case-sensitive in Statsig and case-insensitive in Featureflip", attr))
	case "before", "after":
		for i, v := range values {
			if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
				values[i] = time.UnixMilli(ms).UTC().Format(time.RFC3339)
			}
		}
		emit(im.Warning, "date-attribute",
			fmt.Sprintf("Featureflip reads the date in %q as an ISO 8601 date or Unix seconds; Statsig apps usually send milliseconds", attr))
	case "version_gt", "version_gte", "version_lt", "version_lte", "version_eq":
		for _, v := range values {
			if !strictSemver.MatchString(v) {
				emit(im.Warning, "version-format",
					fmt.Sprintf("it compares %q with the version %q; Featureflip compares semantic versions (major.minor.patch)", attr, v))
				break
			}
		}
	}
	return condResult{cond: im.Condition{Attribute: attr, Operator: op, Values: values, Negate: negatedOperators[c.Operator]}}
}

// unitAttribute is the context attribute holding a Statsig unit type's ID.
func unitAttribute(idType string) string {
	switch idType {
	case "", "userID", "user_id":
		return im.UserID
	}
	return idType
}

// ---------------------------------------------------------------- helpers

// canonicalJSON re-encodes a JSON value with sorted keys, so equal values
// compare equal. A missing value is the empty object, as Statsig shows it.
func canonicalJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}", nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep large integers exact
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func pct(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return strings.Join(q, " and ")
}
