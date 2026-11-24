package launchdarkly

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// maxExpandedRules caps how many Featureflip rules one LaunchDarkly rule may
// become when the segments it names have to be inlined.
const maxExpandedRules = 16

type envInfo struct {
	key string // Featureflip environment key
	ld  string // LaunchDarkly environment key
}

type translator struct {
	rep      *im.Report
	envs     []envInfo
	flagKeys *im.Keyer
	segKeys  *im.Keyer
	// flagKey maps a LaunchDarkly flag key to its Featureflip key. Keyer.Key
	// registers an owner on every call, so each source key is keyed once.
	flagKey map[string]string
	flags   map[string]*flagInfo
	// segs holds each environment's segments, by LaunchDarkly environment key
	// and then segment key.
	segs map[string]map[string]*segInfo
	// unexported says why each segment that isn't created in Featureflip
	// isn't, by LaunchDarkly key; segUsed marks the segments flags use.
	unexported map[string]string
	segUsed    map[string]bool
	// prereqSrc lists each flag's prerequisites, by LaunchDarkly key.
	prereqSrc map[string][]string
	// loose is set once any string comparison is imported.
	loose bool
	// subject is the flag or segment being translated, for notes that are
	// about the item as a whole.
	subjectKind im.Kind
	subjectKey  string
}

// noteKind warns, once per item and kind, that the item reads a non-user
// context kind, which Featureflip reads as flat, prefixed attributes.
func (t *translator) noteKind(kind string) {
	if kind == "" || kind == "user" {
		return
	}
	t.rep.Add(im.Warning, t.subjectKind, t.subjectKey, "context-kind", fmt.Sprintf(
		"it reads %s contexts. Featureflip contexts are flat, so it reads their key as %q and their other attributes as %q, which your app must send",
		kind, kind+".key", kind+".<attribute>"), "")
}

// flagInfo is what other flags' prerequisites need to know about a flag.
type flagInfo struct {
	src      string
	typ      im.FlagType
	keys     []string // Featureflip variation key by LaunchDarkly index
	boolTrue int      // index of the true variation, or -1
	// off is the variation Featureflip serves while the flag is off, by
	// Featureflip environment key.
	off map[string]string
}

// translate turns a snapshot into a Featureflip project and its report.
func translate(snap *snapshot, opts im.Options) (*im.Result, error) {
	t := &translator{
		rep: im.NewReport(), flagKeys: im.NewKeyer(), segKeys: im.NewKeyer(),
		flagKey: map[string]string{}, flags: map[string]*flagInfo{}, segs: map[string]map[string]*segInfo{},
		unexported: map[string]string{}, segUsed: map[string]bool{}, prereqSrc: map[string][]string{},
	}
	project := &im.Project{Segments: []im.Segment{}, Flags: []im.Flag{}}
	if err := t.environments(snap.Environments, opts.EnvMap, project); err != nil {
		return nil, err
	}
	project.Segments = t.segments(snap.Segments)

	for _, f := range snap.Flags {
		// An archived flag isn't imported, so its key can't collide.
		if _, seen := t.flagKey[f.Key]; !seen && !f.Archived {
			t.flagKey[f.Key] = t.flagKeys.Key(f.Key)
		}
	}
	var built []*im.Flag
	var sources []flag
	for _, f := range snap.Flags {
		if f.Archived {
			continue
		}
		built = append(built, t.flag(f))
		sources = append(sources, f)
	}
	for i, f := range built {
		t.prerequisites(sources[i], f)
	}
	for _, f := range built {
		project.Flags = append(project.Flags, *f)
	}
	t.collisions(project)
	// After collisions: a flag that depends on a blocked one is blocked too.
	t.blockDependents(built)
	t.reportUnexported()

	for _, key := range snap.Archived {
		t.rep.Add(im.Info, im.KindProject, "", "archived", fmt.Sprintf("Skipped the archived flag %q.", key), "")
	}
	if t.loose {
		t.rep.Add(im.Warning, im.KindProject, "", "case-insensitive",
			`LaunchDarkly compares strings case-sensitively and never equates a string with a number ("1" is not 1). Featureflip ignores case in In, StartsWith, EndsWith and Contains, and compares numbers by value, so a rule or target can match contexts it didn't match in LaunchDarkly.`, "")
	}
	t.rep.Add(im.Info, im.KindProject, "", "not-read",
		"Experiment results, metrics, scheduled changes, approvals, flag triggers, workflows, release pipelines, holdouts, code references, teams and custom roles are not read. Only flags, their targeting and segments are imported.", "")
	t.rep.Add(im.Info, im.KindProject, "", "rebucketing",
		"Rollouts keep their percentages but not their membership: Featureflip buckets contexts with its own hash, so a 30% rollout reaches a different 30%.", "")
	return &im.Result{Source: "LaunchDarkly", Project: project, Report: t.rep}, nil
}

func (t *translator) environments(envs []environment, envMap map[string]string, project *im.Project) error {
	if len(envs) == 0 {
		return fmt.Errorf("the LaunchDarkly project has no environments")
	}
	known := map[string]bool{}
	byKey := map[string]string{}
	for _, e := range envs {
		known[e.Key] = true
		key := im.NormalizeKey(e.Key)
		if mapped, ok := envMap[e.Key]; ok {
			key = mapped
		}
		if key == "" {
			return im.OptionError{Msg: fmt.Sprintf("the LaunchDarkly environment %q has no usable key: map it with --env-map %s=<key>", e.Key, e.Key)}
		}
		if other, dup := byKey[key]; dup {
			return im.OptionError{Msg: fmt.Sprintf("the LaunchDarkly environments %q and %q both map to %q", other, e.Key, key)}
		}
		byKey[key] = e.Key
		t.envs = append(t.envs, envInfo{key: key, ld: e.Key})
		project.Environments = append(project.Environments, im.Environment{Key: key, Source: e.Key})
	}
	var unknown []string
	for name := range envMap {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		keys := make([]string, 0, len(envs))
		for _, e := range envs {
			keys = append(keys, e.Key)
		}
		return im.OptionError{Msg: fmt.Sprintf("--env-map names %s, which the LaunchDarkly project doesn't have (it has %s)",
			strings.Join(unknown, ", "), strings.Join(keys, ", "))}
	}
	return nil
}

// collisions blocks every flag whose key another flag normalized to as well.
func (t *translator) collisions(p *im.Project) {
	for _, f := range p.Flags {
		if owners := t.flagKeys.Owners(f.Key); len(owners) > 1 {
			t.rep.Add(im.Error, im.KindFlag, f.SourceKey, "key-collision",
				fmt.Sprintf("%s become the same key %q; rename all but one in LaunchDarkly first", quoteList(owners), f.Key), "")
		}
	}
}

// ---------------------------------------------------------------- flags

func (t *translator) flag(f flag) *im.Flag {
	t.subjectKind, t.subjectKey = im.KindFlag, f.Key
	key := t.flagKey[f.Key]
	out := &im.Flag{
		Key: key, SourceKey: f.Key, Name: firstNonEmpty(f.Name, f.Key), Description: f.Description,
		Tags: f.Tags, Environments: map[string]*im.EnvConfig{},
	}
	if f.ClientSideAvailability != nil {
		out.ClientSide = f.ClientSideAvailability.UsingEnvironmentID || f.ClientSideAvailability.UsingMobileKey
	}
	fail := func(code, msg, env string) { t.rep.Add(im.Error, im.KindFlag, f.Key, code, msg, env) }
	switch {
	case key == "":
		fail("key-unusable", "its key has no characters a Featureflip key can use", "")
	case key != f.Key:
		t.rep.Add(im.Warning, im.KindFlag, f.Key, "key-renamed",
			fmt.Sprintf("imported with the key %q, so the code that reads %q must use the new key", key, f.Key), "")
	}
	if f.Deprecated {
		t.rep.Add(im.Info, im.KindFlag, f.Key, "deprecated", "it is deprecated in LaunchDarkly; it is imported as an ordinary flag", "")
	}

	info, err := t.variations(f, out)
	if err != "" {
		fail("unreadable-value", err, "")
		return out
	}
	t.flags[f.Key] = info

	for _, e := range t.envs {
		cfg := f.Environments[e.ld]
		if cfg == nil {
			continue
		}
		emit := func(sev im.Severity, code, msg string) { t.rep.Add(sev, im.KindFlag, f.Key, code, msg, e.key) }
		out.Environments[e.key] = t.envConfig(info, cfg, e, emit)
		unbucketableWarnings(out.Environments[e.key].Rules, emit)
		info.off[e.key] = offServed(info, out.Environments[e.key])
	}
	return out
}

// offServed is what Featureflip serves while the flag is off: false for a
// boolean, the default variation otherwise.
func offServed(info *flagInfo, ec *im.EnvConfig) string {
	if info.typ == im.Boolean {
		return "false"
	}
	return ec.Default
}

// variations sets the flag's type and variations, and returns how
// LaunchDarkly's variation indices map onto them.
func (t *translator) variations(f flag, out *im.Flag) (*flagInfo, string) {
	info := &flagInfo{src: f.Key, boolTrue: -1, off: map[string]string{}}
	if len(f.Variations) == 0 {
		return nil, "it has no variations"
	}
	kinds := map[string]bool{}
	for _, v := range f.Variations {
		kinds[jsonKind(v.Value)] = true
	}
	switch {
	case f.Kind == "boolean" || (len(kinds) == 1 && kinds["bool"]):
		if !(len(kinds) == 1 && kinds["bool"]) {
			return nil, "it is a boolean flag with a variation that is not true or false"
		}
		info.typ = im.Boolean
	case len(kinds) == 1 && kinds["string"]:
		info.typ = im.String
	case len(kinds) == 1 && kinds["number"]:
		info.typ = im.Number
	default:
		info.typ = im.JSON
	}
	out.Type = info.typ

	if info.typ == im.Boolean {
		seen := map[string]bool{}
		for i, v := range f.Variations {
			key := strings.TrimSpace(string(v.Value))
			if seen[key] {
				return nil, "it has two variations with the value " + key
			}
			seen[key] = true
			info.keys = append(info.keys, key)
			if key == "true" {
				info.boolTrue = i
			}
		}
		out.Variations = []im.Variation{{Key: "true", Name: "True", Value: "true"}, {Key: "false", Name: "False", Value: "false"}}
		return info, ""
	}

	used := map[string]bool{}
	for i, v := range f.Variations {
		var value string
		if info.typ == im.String {
			_ = json.Unmarshal(v.Value, &value)
		} else {
			c, err := canonicalJSON(v.Value)
			if err != nil {
				return nil, fmt.Sprintf("variation %d is not valid JSON", i+1)
			}
			value = c
		}
		base := im.NormalizeKey(v.Name)
		if base == "" && info.typ == im.String {
			base = im.NormalizeKey(value)
		}
		if base == "" {
			base = fmt.Sprintf("variation-%d", i+1)
		}
		key := base
		for n := 2; used[key]; n++ {
			key = fmt.Sprintf("%s-%d", base, n)
		}
		used[key] = true
		info.keys = append(info.keys, key)
		out.Variations = append(out.Variations, im.Variation{Key: key, Name: firstNonEmpty(v.Name, key), Value: value})
	}
	return info, ""
}

// envConfig translates a flag's configuration in one environment. LaunchDarkly
// evaluates off, prerequisites, individual targets, rules and then the default
// rule; Featureflip has the same order but no separate off variation, so the
// default holds the off variation and a final rule serves the default rule
// when they differ.
func (t *translator) envConfig(info *flagInfo, cfg *flagEnv, e envInfo, emit func(im.Severity, string, string)) *im.EnvConfig {
	out := &im.EnvConfig{Enabled: cfg.On, Rules: t.targetRules(info, cfg, emit)}
	for i, r := range cfg.Rules {
		out.Rules = append(out.Rules, t.rule(info, r, i, e.ld, emit)...)
	}

	ft, ok := t.serveOf(info, cfg.Fallthrough.Variation, cfg.Fallthrough.Rollout, "the default rule", emit)
	if !ok {
		return out
	}
	ft.Description = "Default rule"
	ft.Groups = []im.ConditionGroup{}
	// A context without the bucket-by attribute skips a Featureflip rollout;
	// the last rule's then lands on the default. LaunchDarkly gives it bucket
	// 0: the first variation the split serves.
	unbucketable := func(deflt string) {
		if ft.Rollout == nil {
			return
		}
		for _, w := range cfg.Fallthrough.Rollout.Variations {
			if w.Weight > 0 && w.Variation >= 0 && w.Variation < len(info.keys) {
				if first := info.keys[w.Variation]; first != deflt {
					emit(im.Warning, "unbucketable-default", fmt.Sprintf(
						"the default rule's rollout: a context without %q gets the default variation %q in Featureflip, and %q in LaunchDarkly",
						ft.BucketBy, deflt, first))
				}
				return
			}
		}
	}

	var off string
	if cfg.OffVariation != nil && *cfg.OffVariation >= 0 && *cfg.OffVariation < len(info.keys) {
		off = info.keys[*cfg.OffVariation]
	}
	offProblem := func(msg string) {
		if !cfg.On {
			emit(im.Error, "off-variation", msg+". Change it in LaunchDarkly, or turn the flag on there, and re-run")
		} else {
			emit(im.Warning, "off-variation", msg+", so turning the flag off in Featureflip changes what it serves")
		}
	}

	if info.typ == im.Boolean {
		if off != "false" {
			what := "is true"
			if off == "" {
				what = "is not set, so LaunchDarkly serves your code's fallback value while the flag is off"
			}
			offProblem("its off variation in LaunchDarkly " + what + ", and Featureflip serves false while a boolean flag is off")
		}
		if ft.Rollout == nil {
			out.Default = ft.Variation
			return out
		}
		out.Default = map[string]string{"true": "false", "false": "true"}[ft.Variation]
		out.Rules = append(out.Rules, ft)
		unbucketable(out.Default)
		return out
	}

	if off == "" {
		offProblem("it has no off variation in LaunchDarkly, which then serves your code's fallback value while the flag is off; Featureflip serves its default variation")
		off = ft.Variation
	}
	out.Default = off
	if ft.Rollout != nil || ft.Variation != off {
		out.Rules = append(out.Rules, ft)
		unbucketable(off)
	}
	return out
}

// targetRules turns individual targets into leading rules, in the order
// LaunchDarkly checks them. A user-kind entry in contextTargets with no values
// stands for the user targets of its variation.
func (t *translator) targetRules(info *flagInfo, cfg *flagEnv, emit func(im.Severity, string, string)) []im.Rule {
	rules := []im.Rule{}
	add := func(kind string, values []string, variation int) {
		if len(values) == 0 {
			return
		}
		if variation < 0 || variation >= len(info.keys) {
			emit(im.Error, "unreadable-value", fmt.Sprintf("its individual targets serve variation %d, which the flag doesn't have", variation))
			return
		}
		attr := im.UserID
		if kind != "" && kind != "user" {
			attr = kind + ".key"
		}
		t.noteKind(kind)
		t.loose = true
		rules = append(rules, im.Rule{
			Description: "Individual targets",
			Groups:      []im.ConditionGroup{{Operator: im.And, Conditions: []im.Condition{{Attribute: attr, Operator: im.OpIn, Values: append([]string{}, values...)}}}},
			Variation:   info.keys[variation],
		})
	}
	done := map[int]bool{}
	userTargets := func(variation int) {
		for _, tg := range cfg.Targets {
			if tg.Variation == variation && !done[variation] {
				add("user", tg.Values, variation)
			}
		}
		done[variation] = true
	}
	for _, ct := range cfg.ContextTargets {
		if (ct.ContextKind == "" || ct.ContextKind == "user") && len(ct.Values) == 0 {
			userTargets(ct.Variation)
			continue
		}
		add(ct.ContextKind, ct.Values, ct.Variation)
	}
	for _, tg := range cfg.Targets {
		userTargets(tg.Variation)
	}
	return rules
}

// rule translates one LaunchDarkly rule for one environment.
func (t *translator) rule(info *flagInfo, r flagRule, index int, ld string, envEmit func(im.Severity, string, string)) []im.Rule {
	label := firstNonEmpty(r.Description, fmt.Sprintf("rule %d", index+1))
	emit := func(sev im.Severity, code, msg string) { envEmit(sev, code, fmt.Sprintf("%s: %s", label, msg)) }
	if r.Disabled {
		emit(im.Warning, "rule-disabled", "it is disabled in LaunchDarkly, so it is left out")
		return nil
	}

	var base []im.Condition
	var groups []im.ConditionGroup
	var disjunctions [][][]im.Condition
	var segRef *segInfo
	soleSegment := len(r.Clauses) == 1

	for _, c := range r.Clauses {
		if c.Op == "segmentMatch" {
			use, ok := t.segmentClause(c, soleSegment, ld, emit)
			if !ok {
				return nil
			}
			switch {
			case use.ref != nil:
				segRef = use.ref
			case use.never:
				return nil
			}
			base = append(base, use.base...)
			groups = append(groups, use.groups...)
			if use.alts != nil {
				disjunctions = append(disjunctions, use.alts)
			}
			continue
		}
		res := t.clause(c, emit)
		switch {
		case res.err != "":
			emit(im.Error, res.code, res.err)
			return nil
		case res.never:
			emit(im.Info, "never-matches", "it compares against no values, so it never matches and is left out")
			return nil
		case res.always:
			continue
		}
		base = append(base, res.conds...)
	}

	serve, ok := t.serveOf(info, r.Variation, r.Rollout, label, emit)
	if !ok {
		return nil
	}
	serve.Description = r.Description

	if segRef != nil {
		serve.SegmentKey = segRef.key
		serve.Groups = []im.ConditionGroup{}
		return []im.Rule{serve}
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
		if all := append(append([]im.Condition{}, base...), extra...); len(all) > 0 {
			rl.Groups = append(rl.Groups, im.ConditionGroup{Operator: im.And, Conditions: all})
		}
		rl.Groups = append(rl.Groups, groups...)
		out = append(out, rl)
	}
	return out
}

// unbucketableWarnings flags rollout rules with rules after them. A context
// without the bucket-by attribute skips a Featureflip rollout and reaches the
// later rules, while LaunchDarkly stops at the rule that matched.
func unbucketableWarnings(rules []im.Rule, emit func(im.Severity, string, string)) {
	for i, r := range rules {
		if r.Rollout != nil && i < len(rules)-1 {
			emit(im.Warning, "unbucketable-falls-through",
				fmt.Sprintf("rule %q: a context without %q skips this rollout and reaches the rules after it; LaunchDarkly would stop here", firstNonEmpty(r.Description, fmt.Sprintf("%d", i+1)), r.BucketBy))
		}
	}
}

// ---------------------------------------------------------------- helpers

// jsonKind names a JSON value's type: bool, string, number, null or json.
func jsonKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "true" || s == "false":
		return "bool"
	case s == "null" || s == "":
		return "null"
	case strings.HasPrefix(s, `"`):
		return "string"
	case strings.HasPrefix(s, "{") || strings.HasPrefix(s, "["):
		return "json"
	}
	return "number"
}

// canonicalJSON re-encodes a JSON value with sorted keys, so equal values
// compare equal.
func canonicalJSON(raw json.RawMessage) (string, error) {
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
