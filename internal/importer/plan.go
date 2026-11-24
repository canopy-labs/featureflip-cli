package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
)

// ApplyOptions are the choices that only matter when writing.
type ApplyOptions struct {
	Org, Project string
	// Overwrite replaces the targeting of flags, and the conditions of
	// segments, that already exist in Featureflip and differ from the source.
	Overwrite bool
	// CarryState turns each flag on in the environments where it is on in the
	// source. Without it every imported flag arrives off.
	CarryState bool
	// ClientSide makes created flags available to client-side SDKs.
	ClientSide bool
	// Reason is sent as the change reason on every write.
	Reason string
	// BreakFreeze sends X-Freeze-Override: true on every write, so an Owner or
	// Admin can import into an environment a change freeze covers.
	BreakFreeze bool
}

// Action is what the import does with one item.
type Action string

const (
	// ActCreate: the item doesn't exist in Featureflip yet.
	ActCreate Action = "create"
	// ActUpdate: it exists, differs, and --overwrite replaces it.
	ActUpdate Action = "update"
	// ActUnchanged: it exists and already matches the source.
	ActUnchanged Action = "unchanged"
	// ActDiffers: it exists and differs, and is left alone without --overwrite.
	ActDiffers Action = "differs"
	// ActConflict: it exists in a shape the import can't update.
	ActConflict Action = "conflict"
	// ActBlocked: the translation report blocks it, or it needs a segment
	// that isn't being written.
	ActBlocked Action = "blocked"
)

// Step is one item of a plan.
type Step struct {
	Kind   Kind   `json:"kind"`
	Key    string `json:"key"`
	Action Action `json:"action"`
	// Why explains every action other than create and unchanged.
	Why string `json:"why,omitempty"`
	// Environments are the environments an update writes, or that differ.
	Environments []string `json:"environments,omitempty"`
	// Done is true once every write of the step has succeeded.
	Done bool `json:"done"`
	// Created is true once a flag the step creates exists in Featureflip,
	// even when a later write of its environments failed. Created and not
	// Done is a flag left part configured, which only --overwrite finishes.
	Created bool `json:"created,omitempty"`

	segment *Segment
	flag    *Flag
	// existing is the current state of a flag that exists in Featureflip.
	existing *flagState
}

func (s *Step) writes() bool { return s.Action == ActCreate || s.Action == ActUpdate }

// Plan is what an import will do to one Featureflip project. Build it with
// BuildPlan, then Apply it.
type Plan struct {
	Org     string  `json:"org"`
	Project string  `json:"project"`
	Steps   []*Step `json:"steps"`
	// FlagsInProject counts the project's flags that count against the plan's
	// flag cap (archived ones don't). FlagCap is nil when there is no cap or
	// the server doesn't say.
	FlagsInProject int    `json:"flagsInProject"`
	FlagCap        *int   `json:"flagCap,omitempty"`
	PlanName       string `json:"plan"`

	opts   ApplyOptions
	source string
	envs   []Environment
	// confirm holds the target environments that require confirmation.
	confirm map[string]bool
	// segIDs maps a segment key to its id, filled for existing segments while
	// planning and for created ones while applying.
	segIDs map[string]string
	// segKeys maps an existing segment's id back to its key.
	segKeys map[string]string
}

// flagState is what an existing flag looks like in Featureflip.
type flagState struct {
	varIDs  map[string]string // variation key -> id
	varKeys map[string]string // variation id -> key
	envs    map[string]envState
}

type envState struct {
	enabled bool
	rules   []string // canonical rule signatures, in order
	deflt   string   // default variation key; "" when it isn't a single variation
	// prereqs are the flag's prerequisites there, as sorted prereqSigs.
	prereqs []string
}

// LimitError means the import would take the project past its plan's flag
// cap. It is returned before anything is written.
type LimitError struct {
	Plan              string
	Cap, InUse, Needs int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("this import creates %d flags, but the %s plan allows %d per project and the project already has %d. "+
		"Nothing was written. Upgrade the plan, or import into a project with room",
		e.Needs, e.Plan, e.Cap, e.InUse)
}
func (*LimitError) ExitCode() int { return exitcode.Rejected }

// BuildPlan reads the target project and decides what the import does with
// each translated item. It only reads.
func BuildPlan(ctx context.Context, c *api.Client, res *Result, opts ApplyOptions) (*Plan, error) {
	res.Summarize()
	p := &Plan{
		Org: opts.Org, Project: opts.Project, opts: opts, source: res.Source,
		envs: res.Project.Environments, segIDs: map[string]string{}, segKeys: map[string]string{},
		confirm: map[string]bool{},
	}
	projectPath := api.P("orgs", opts.Org, "projects", opts.Project)

	envs, err := api.ListAll[api.Environment](ctx, c, append(projectPath, "environments"), nil)
	if err != nil {
		return nil, err
	}
	if err := checkEnvironments(res.Project.Environments, envs, opts.Project); err != nil {
		return nil, err
	}
	for _, e := range envs {
		if e.RequireConfirmation {
			p.confirm[e.Key] = true
		}
	}

	var org api.Org
	if err := c.Do(ctx, "GET", api.P("orgs", opts.Org), api.Request{}, &org); err != nil {
		return nil, err
	}
	p.PlanName = org.Plan
	if org.Limits != nil {
		p.FlagCap = org.Limits.FlagsPerProject
	}

	flags, err := api.ListAll[api.FlagListItem](ctx, c, append(projectPath, "flags"), nil)
	if err != nil {
		return nil, err
	}
	existingFlags := map[string]api.FlagListItem{}
	for _, f := range flags {
		existingFlags[f.Key] = f
		if !f.IsArchived {
			p.FlagsInProject++
		}
	}

	segments, err := api.ListAll[api.Segment](ctx, c, append(projectPath, "segments"), nil)
	if err != nil {
		return nil, err
	}
	existingSegs := map[string]api.Segment{}
	for _, s := range segments {
		existingSegs[s.Key] = s
		p.segKeys[s.ID] = s.Key
	}

	segAction := map[string]Action{}
	for i := range res.Project.Segments {
		sg := &res.Project.Segments[i]
		st := &Step{Kind: KindSegment, Key: sg.Key, segment: sg}
		ex, exists := existingSegs[sg.Key]
		switch {
		case sg.Blocked:
			st.Action, st.Why = ActBlocked, "the report blocks it"
		case !exists:
			st.Action = ActCreate
		case sameConditions(sg.Conditions, ex.Conditions):
			st.Action = ActUnchanged
			p.segIDs[sg.Key] = ex.ID
		case opts.Overwrite:
			st.Action, st.Why = ActUpdate, "its conditions in Featureflip differ; --overwrite replaces them"
			p.segIDs[sg.Key] = ex.ID
		default:
			st.Action, st.Why = ActDiffers, "a segment with this key exists in Featureflip with other conditions. Pass --overwrite to replace them"
		}
		segAction[sg.Key] = st.Action
		p.Steps = append(p.Steps, st)
	}

	for i := range res.Project.Flags {
		f := &res.Project.Flags[i]
		st := &Step{Kind: KindFlag, Key: f.Key, flag: f}
		p.Steps = append(p.Steps, st)
		if f.Blocked {
			st.Action, st.Why = ActBlocked, "the report blocks it"
			continue
		}
		if seg, why := unavailableSegment(f, segAction); seg != "" {
			st.Action, st.Why = ActBlocked, fmt.Sprintf("it uses segment %s, which %s", seg, why)
			continue
		}
		ex, exists := existingFlags[f.Key]
		if !exists {
			st.Action = ActCreate
			continue
		}
		if ex.IsArchived {
			st.Action, st.Why = ActConflict, "a flag with this key is archived in Featureflip. Restore or delete it, then re-run"
			continue
		}
		if err := p.compareFlag(ctx, c, st); err != nil {
			return nil, err
		}
	}
	blockOnPrerequisites(p.Steps)
	if err := orderByPrerequisites(p); err != nil {
		return nil, err
	}
	return p, nil
}

// blockOnPrerequisites blocks every flag whose prerequisite won't be in
// Featureflip as translated, repeating until a whole chain is blocked.
func blockOnPrerequisites(steps []*Step) {
	flags := map[string]*Step{}
	for _, st := range steps {
		if st.Kind == KindFlag {
			flags[st.Key] = st
		}
	}
	for changed := true; changed; {
		changed = false
		for _, st := range steps {
			if st.Kind != KindFlag || st.Action == ActBlocked || st.Action == ActConflict {
				continue
			}
			if key, why := unavailablePrerequisite(st.flag, flags); key != "" {
				st.Action, st.Why, st.Environments = ActBlocked, fmt.Sprintf("its prerequisite %s %s", key, why), nil
				changed = true
			}
		}
	}
}

func unavailablePrerequisite(f *Flag, flags map[string]*Step) (key, why string) {
	for _, env := range sortedEnvKeys(f.Environments) {
		for _, pr := range f.Environments[env].Prerequisites {
			st, ok := flags[pr.Flag]
			switch {
			case !ok:
				return pr.Flag, "isn't in the import"
			case st.Action == ActBlocked:
				return pr.Flag, "is blocked"
			case st.Action == ActConflict:
				return pr.Flag, "is in conflict"
			}
		}
	}
	return "", ""
}

// orderByPrerequisites puts segments first, then every flag after the flags
// it depends on, because the API refuses a prerequisite that doesn't exist.
// Otherwise the translation's order is kept.
func orderByPrerequisites(p *Plan) error {
	var out, flags []*Step
	byKey := map[string]*Step{}
	for _, st := range p.Steps {
		if st.Kind == KindFlag {
			flags = append(flags, st)
			byKey[st.Key] = st
		} else {
			out = append(out, st)
		}
	}
	const visiting, done = 1, 2
	state := map[string]int{}
	var visit func(st *Step, path []string) error
	visit = func(st *Step, path []string) error {
		switch state[st.Key] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("the flags %s depend on each other through prerequisites", strings.Join(append(path, st.Key), " -> "))
		}
		state[st.Key] = visiting
		// Only a step that writes needs its prerequisites written first; a
		// blocked one may sit on a cycle the translation already reported.
		for _, env := range sortedEnvKeys(st.flag.Environments) {
			if !st.writes() {
				break
			}
			for _, pr := range st.flag.Environments[env].Prerequisites {
				if dep, ok := byKey[pr.Flag]; ok {
					if err := visit(dep, append(path, st.Key)); err != nil {
						return err
					}
				}
			}
		}
		state[st.Key] = done
		out = append(out, st)
		return nil
	}
	for _, st := range flags {
		if err := visit(st, nil); err != nil {
			return err
		}
	}
	p.Steps = out
	return nil
}

// prereqSigs is a translated environment's prerequisites in a canonical
// form. All of them must pass, so their order carries no meaning.
func prereqSigs(ps []Prerequisite) []string {
	out := make([]string, 0, len(ps))
	for _, pr := range ps {
		out = append(out, pr.Flag+"="+pr.Variation)
	}
	sort.Strings(out)
	return out
}

func actualPrereqSigs(ps []api.Prerequisite) []string {
	out := make([]string, 0, len(ps))
	for _, pr := range ps {
		out = append(out, pr.PrerequisiteFlagKey+"="+pr.ExpectedVariationKey)
	}
	sort.Strings(out)
	return out
}

// checkEnvironments fails when a translated environment has no Featureflip
// environment to land in.
func checkEnvironments(want []Environment, have []api.Environment, project string) error {
	known := map[string]bool{}
	keys := make([]string, 0, len(have))
	for _, e := range have {
		known[e.Key] = true
		keys = append(keys, e.Key)
	}
	var missing []string
	for _, e := range want {
		if !known[e.Key] {
			missing = append(missing, fmt.Sprintf("%s (from %s)", e.Key, e.Source))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(keys)
	return OptionError{fmt.Sprintf(
		"project %s has no environment %s. Its environments are: %s. Map each source environment onto one with --env-map source=featureflip, or create the environment first",
		project, strings.Join(missing, ", "), strings.Join(keys, ", "))}
}

// unavailableSegment names a segment the flag's rules use that won't exist
// as translated when the flag is written.
func unavailableSegment(f *Flag, segAction map[string]Action) (key, why string) {
	for _, env := range sortedEnvKeys(f.Environments) {
		for _, r := range f.Environments[env].Rules {
			if r.SegmentKey == "" {
				continue
			}
			switch segAction[r.SegmentKey] {
			case ActDiffers:
				return r.SegmentKey, "differs in Featureflip and is left as it is"
			case ActBlocked:
				return r.SegmentKey, "is blocked"
			case "":
				return r.SegmentKey, "the import doesn't write"
			}
		}
	}
	return "", ""
}

// compareFlag reads an existing flag and decides whether it matches.
func (p *Plan) compareFlag(ctx context.Context, c *api.Client, st *Step) error {
	f := st.flag
	flagPath := api.P("orgs", p.Org, "projects", p.Project, "flags", f.Key)
	var ex api.Flag
	if err := c.Do(ctx, "GET", flagPath, api.Request{}, &ex); err != nil {
		return err
	}
	if why := variationMismatch(f, &ex); why != "" {
		st.Action, st.Why = ActConflict, why+". The import never changes a flag's variations; rename or delete the Featureflip flag, then re-run"
		return nil
	}
	state := &flagState{varIDs: map[string]string{}, varKeys: map[string]string{}, envs: map[string]envState{}}
	for _, v := range ex.Variations {
		state.varIDs[v.Key] = v.ID
		state.varKeys[v.ID] = v.Key
	}
	// Unlike the list routes, this one returns a bare array, not a page.
	var configs []api.FlagEnvConfig
	if err := c.Do(ctx, "GET", append(flagPath, "environments"), api.Request{}, &configs); err != nil {
		return err
	}
	byEnv := map[string]api.FlagEnvConfig{}
	for _, cfg := range configs {
		byEnv[cfg.EnvironmentKey] = cfg
	}
	for _, env := range p.envs {
		var tc api.TargetingConfig
		if err := c.Do(ctx, "GET", append(flagPath, "environments", env.Key, "targeting"), api.Request{}, &tc); err != nil {
			return err
		}
		es := envState{enabled: tc.IsEnabled}
		for _, r := range tc.Rules {
			es.rules = append(es.rules, p.actualRuleSig(r, state))
		}
		if cfg, ok := byEnv[env.Key]; ok {
			if cfg.Strategy == "SingleVariation" {
				es.deflt = state.varKeys[cfg.DefaultVariationID]
			}
			es.prereqs = actualPrereqSigs(cfg.Prerequisites)
		}
		state.envs[env.Key] = es
	}
	st.existing = state

	for _, env := range p.envs {
		if !p.envMatches(wantEnv(f, env.Key), state.envs[env.Key]) {
			st.Environments = append(st.Environments, env.Key)
		}
	}
	switch {
	case len(st.Environments) == 0:
		st.Action = ActUnchanged
	case p.opts.Overwrite:
		st.Action, st.Why = ActUpdate, "its targeting differs; --overwrite replaces it"
	default:
		st.Action, st.Why = ActDiffers, "a flag with this key exists in Featureflip with other targeting. Pass --overwrite to replace it"
	}
	return nil
}

func (p *Plan) envMatches(want *EnvConfig, have envState) bool {
	if want.Default != have.deflt || !slices.Equal(prereqSigs(want.Prerequisites), have.prereqs) {
		return false
	}
	if p.opts.CarryState && want.Enabled != have.enabled {
		return false
	}
	if len(want.Rules) != len(have.rules) {
		return false
	}
	for i, r := range want.Rules {
		if ruleSig(r) != have.rules[i] {
			return false
		}
	}
	return true
}

// variationMismatch says how the existing flag's type or variations differ.
func variationMismatch(f *Flag, ex *api.Flag) string {
	if string(f.Type) != ex.Type {
		return fmt.Sprintf("a %s flag with this key exists in Featureflip, and this one is %s", ex.Type, f.Type)
	}
	have := map[string]string{}
	for _, v := range ex.Variations {
		have[v.Key] = canonicalValue(v.Value)
	}
	if len(have) != len(f.Variations) {
		return "a flag with this key exists in Featureflip with other variations"
	}
	for _, v := range f.Variations {
		if got, ok := have[v.Key]; !ok || got != canonicalValue(v.Value) {
			return "a flag with this key exists in Featureflip with other variations"
		}
	}
	return ""
}

func canonicalValue(v string) string {
	var anyV any
	if json.Unmarshal([]byte(v), &anyV) != nil {
		return v
	}
	out, _ := json.Marshal(anyV)
	return string(out)
}

// ruleSig is a rule's identity in keys, so a translated rule and one read
// back from the API compare equal exactly when they behave the same.
type ruleShape struct {
	Description string           `json:"d"`
	Variation   string           `json:"v"`
	Rollout     *int             `json:"r"`
	Segment     string           `json:"s"`
	BucketBy    string           `json:"b"`
	Groups      []ConditionGroup `json:"g"`
}

func ruleSig(r Rule) string {
	shape := ruleShape{
		Description: r.Description, Variation: r.Variation, Rollout: r.Rollout,
		Segment: r.SegmentKey, BucketBy: normalBucketBy(r.BucketBy), Groups: normalGroups(r.Groups),
	}
	if r.SegmentKey != "" {
		shape.Groups = []ConditionGroup{}
	}
	out, _ := json.Marshal(shape)
	return string(out)
}

func (p *Plan) actualRuleSig(r api.TargetingRule, state *flagState) string {
	shape := ruleShape{
		Variation: state.varKeys[r.VariationID], Rollout: r.RolloutPercentage,
		BucketBy: normalBucketBy(r.BucketBy), Groups: []ConditionGroup{},
	}
	if r.Description != nil {
		shape.Description = *r.Description
	}
	if r.UserSegmentID != nil && *r.UserSegmentID != "" {
		shape.Segment = p.segKeys[*r.UserSegmentID]
		if shape.Segment == "" {
			shape.Segment = "id:" + *r.UserSegmentID
		}
	} else {
		for _, g := range r.ConditionGroups {
			cg := ConditionGroup{Operator: GroupOperator(g.Operator)}
			for _, c := range g.Conditions {
				cg.Conditions = append(cg.Conditions, Condition{
					Attribute: c.Attribute, Operator: Operator(c.Operator), Values: c.Values, Negate: c.Negate,
				})
			}
			shape.Groups = append(shape.Groups, cg)
		}
		shape.Groups = normalGroups(shape.Groups)
	}
	out, _ := json.Marshal(shape)
	return string(out)
}

func normalBucketBy(b string) string {
	if b == "" || b == "userId" {
		return UserID
	}
	return b
}

// normalGroups puts groups into a canonical order. Order carries no meaning
// inside a rule (groups are ANDed, a group's conditions share one operator, and
// a condition matches any of its values), and the API doesn't keep the order
// it was given, so comparing in written order would report every re-run of a
// multi-condition rule as a change.
func normalGroups(gs []ConditionGroup) []ConditionGroup {
	out := make([]ConditionGroup, 0, len(gs))
	for _, g := range gs {
		cg := ConditionGroup{Operator: g.Operator, Conditions: normalConditions(g.Conditions)}
		out = append(out, cg)
	}
	sort.Slice(out, func(i, j int) bool { return sigOf(out[i]) < sigOf(out[j]) })
	return out
}

func normalConditions(cs []Condition) []Condition {
	out := make([]Condition, 0, len(cs))
	for _, c := range cs {
		c.Values = append([]string{}, c.Values...)
		sort.Strings(c.Values)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return sigOf(out[i]) < sigOf(out[j]) })
	return out
}

func sigOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// sameConditions compares a segment's conditions, which it ANDs, in any order.
func sameConditions(want []Condition, have []api.SegmentCondition) bool {
	got := make([]Condition, 0, len(have))
	for _, h := range have {
		got = append(got, Condition{Attribute: h.Attribute, Operator: Operator(h.Operator), Values: h.Values, Negate: h.Negate})
	}
	return sigOf(normalConditions(want)) == sigOf(normalConditions(got))
}

func sortedEnvKeys(m map[string]*EnvConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EnvironmentsToConfirm lists the environments that require confirmation in
// which applying the plan changes what live traffic gets: an existing flag's
// targeting or state, a segment existing flags may use, or a created flag
// turned on by --carry-state. Creating a flag that stays off changes nothing
// live, so it needs no confirmation.
func (p *Plan) EnvironmentsToConfirm() []string {
	touched := map[string]bool{}
	for _, st := range p.Steps {
		switch {
		case st.Kind == KindSegment && st.Action == ActUpdate:
			for _, e := range p.envs {
				touched[e.Key] = true
			}
		case st.Kind == KindFlag && st.Action == ActUpdate:
			for _, e := range st.Environments {
				touched[e] = true
			}
		case st.Kind == KindFlag && st.Action == ActCreate && p.opts.CarryState:
			for env, cfg := range st.flag.Environments {
				if cfg != nil && cfg.Enabled {
					touched[env] = true
				}
			}
		}
	}
	var out []string
	for _, e := range p.envs {
		if touched[e.Key] && p.confirm[e.Key] {
			out = append(out, e.Key)
		}
	}
	return out
}

// CheckLimits returns a *LimitError when creating the plan's flags would go
// past the plan's per-project flag cap.
func (p *Plan) CheckLimits() error {
	if p.FlagCap == nil {
		return nil
	}
	needs := 0
	for _, st := range p.Steps {
		if st.Kind == KindFlag && st.Action == ActCreate {
			needs++
		}
	}
	if needs > 0 && p.FlagsInProject+needs > *p.FlagCap {
		return &LimitError{Plan: p.PlanName, Cap: *p.FlagCap, InUse: p.FlagsInProject, Needs: needs}
	}
	return nil
}
