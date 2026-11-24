package importer

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
)

// ApplyError is a write that failed part way through an import. Everything
// before Step was written; Step and everything after it was not.
type ApplyError struct {
	Step *Step
	Err  error
	// Plan is the plan as far as it got, for the --json error document.
	Plan *Plan
}

func (e *ApplyError) Error() string {
	msg := fmt.Sprintf("writing %s %s failed: %v", e.Step.Kind, e.Step.Key, e.Err)
	switch {
	case e.Step.Created:
		msg += fmt.Sprintf(". %s was created but not fully configured; re-run with --overwrite to finish it", e.Step.Key)
	case e.Step.Kind == KindFlag && e.Step.Action == ActCreate:
		msg += fmt.Sprintf(". If %s was created before the failure, re-run with --overwrite to finish it", e.Step.Key)
	}
	return msg
}
func (e *ApplyError) Unwrap() error { return e.Err }
func (e *ApplyError) ExitCode() int { return exitcode.Classify(e.Err) }

// Apply writes the plan: segments first, then each flag with its targeting.
// It checks the plan's flag cap before the first write and stops at the first
// failed write. Re-running the same import is safe because the plan reads the
// project first: a finished item reads as unchanged. Each create carries its
// own Idempotency-Key, so the client's retry of a create whose response was
// lost is replayed by the server rather than repeated.
func (p *Plan) Apply(ctx context.Context, c *api.Client, progress func(*Step)) error {
	if err := p.CheckLimits(); err != nil {
		return err
	}
	if progress == nil {
		progress = func(*Step) {}
	}
	for _, st := range p.Steps {
		if !st.writes() {
			continue
		}
		var err error
		switch st.Kind {
		case KindSegment:
			err = p.writeSegment(ctx, c, st)
		case KindFlag:
			err = p.writeFlag(ctx, c, st)
		}
		if err != nil {
			return &ApplyError{Step: st, Err: err, Plan: p}
		}
		st.Done = true
		progress(st)
	}
	return nil
}

func (p *Plan) reason() string {
	if p.opts.Reason != "" {
		return p.opts.Reason
	}
	return "Imported from " + p.source + " with featureflip import"
}

// req applies the import-wide freeze override to a write.
func (p *Plan) req(r api.Request) api.Request {
	r.FreezeOverride = p.opts.BreakFreeze
	return r
}

func (p *Plan) path(rest ...string) api.Path {
	return append(api.P("orgs", p.Org, "projects", p.Project), rest...)
}

func (p *Plan) writeSegment(ctx context.Context, c *api.Client, st *Step) error {
	sg := st.segment
	conds := make([]api.SegmentCondition, 0, len(sg.Conditions))
	for _, cd := range sg.Conditions {
		conds = append(conds, api.SegmentCondition{Attribute: cd.Attribute, Operator: string(cd.Operator), Values: nonNil(cd.Values), Negate: cd.Negate})
	}
	desc := optional(sg.Description)
	if st.Action == ActUpdate {
		body := api.UpdateSegmentRequest{Name: sg.Name, Description: desc, Conditions: conds}
		return c.Do(ctx, "PUT", p.path("segments", sg.Key), p.req(api.Request{Body: body, Reason: p.reason()}), nil)
	}
	body := api.CreateSegmentRequest{Key: sg.Key, Name: sg.Name, Description: desc, Conditions: conds}
	var created api.Segment
	err := c.Do(ctx, "POST", p.path("segments"),
		p.req(api.Request{Body: body, Reason: p.reason(), IdempotencyKey: api.NewIdempotencyKey()}), &created)
	if err != nil {
		return err
	}
	if created.ID == "" {
		return errors.New("the API returned no segment id")
	}
	p.segIDs[sg.Key] = created.ID
	return nil
}

func (p *Plan) writeFlag(ctx context.Context, c *api.Client, st *Step) error {
	f := st.flag
	var varIDs map[string]string
	envs := st.Environments
	if st.Action == ActCreate {
		created, err := p.createFlag(ctx, c, f)
		if err != nil {
			return err
		}
		st.Created = true
		varIDs = map[string]string{}
		for _, v := range created.Variations {
			varIDs[v.Key] = v.ID
		}
		envs = nil
		for _, e := range p.envs {
			envs = append(envs, e.Key)
		}
	} else {
		varIDs = st.existing.varIDs
	}

	for _, env := range envs {
		want := wantEnv(f, env)
		var have *envState
		if st.existing != nil {
			h := st.existing.envs[env]
			have = &h
		}
		if err := p.writeEnv(ctx, c, f, env, want, have, varIDs); err != nil {
			return fmt.Errorf("environment %s: %w", env, err)
		}
	}
	return nil
}

func (p *Plan) createFlag(ctx context.Context, c *api.Client, f *Flag) (*api.Flag, error) {
	req := api.CreateFlagRequest{
		Key: f.Key, Name: f.Name, Type: string(f.Type), Description: optional(f.Description),
		Tags: f.Tags, ClientSideVisible: p.opts.ClientSide || f.ClientSide,
	}
	// A boolean flag gets its true/false variations from the server.
	if f.Type != Boolean {
		for _, v := range f.Variations {
			req.InitialVariations = append(req.InitialVariations, api.InitialVariation{Key: v.Key, Name: v.Name, Value: v.Value})
		}
	}
	var created api.Flag
	err := c.Do(ctx, "POST", p.path("flags"),
		p.req(api.Request{Body: req, IdempotencyKey: api.NewIdempotencyKey()}), &created)
	if err != nil {
		return nil, err
	}
	for _, v := range f.Variations {
		if !hasVariation(created.Variations, v.Key) {
			return nil, fmt.Errorf("the created flag has no variation %q", v.Key)
		}
	}
	return &created, nil
}

// writeEnv brings one environment of a flag to the translated configuration.
// have is the current state of an existing flag, nil for one just created.
func (p *Plan) writeEnv(ctx context.Context, c *api.Client, f *Flag, env string, want *EnvConfig, have *envState, varIDs map[string]string) error {
	flagEnv := p.path("flags", f.Key, "environments", env)
	rulesDiffer := have == nil && len(want.Rules) > 0
	if have != nil {
		rulesDiffer = len(want.Rules) != len(have.rules)
		for i := 0; !rulesDiffer && i < len(want.Rules); i++ {
			rulesDiffer = ruleSig(want.Rules[i]) != have.rules[i]
		}
	}
	if rulesDiffer {
		body := api.PutTargetingRequest{Rules: make([]api.RuleRequest, 0, len(want.Rules))}
		for _, r := range want.Rules {
			rr, err := p.ruleRequest(r, varIDs)
			if err != nil {
				return err
			}
			body.Rules = append(body.Rules, rr)
		}
		if err := c.Do(ctx, "PUT", append(flagEnv, "targeting"), p.req(api.Request{Body: body, Reason: p.reason()}), nil); err != nil {
			return err
		}
	}

	// A new flag starts on its server-chosen default, which for a boolean is
	// false; anything else is set explicitly.
	defaultDiffers := have == nil && !(f.Type == Boolean && want.Default == "false")
	prereqsDiffer := have == nil && len(want.Prerequisites) > 0
	if have != nil {
		defaultDiffers = have.deflt != want.Default
		prereqsDiffer = !slices.Equal(prereqSigs(want.Prerequisites), have.prereqs)
	}
	if defaultDiffers || prereqsDiffer {
		id, ok := varIDs[want.Default]
		if !ok {
			return fmt.Errorf("no variation %q to serve by default", want.Default)
		}
		single := "SingleVariation"
		body := api.UpdateFlagEnvConfigRequest{DefaultVariationID: id, Strategy: &single}
		if prereqsDiffer {
			list := make([]api.Prerequisite, 0, len(want.Prerequisites))
			for _, pr := range want.Prerequisites {
				list = append(list, api.Prerequisite{PrerequisiteFlagKey: pr.Flag, ExpectedVariationKey: pr.Variation})
			}
			body.Prerequisites = &list
		}
		if err := c.Do(ctx, "PUT", flagEnv, p.req(api.Request{Body: body, Reason: p.reason()}), nil); err != nil {
			return err
		}
	}

	if p.opts.CarryState {
		current := false
		if have != nil {
			current = have.enabled
		}
		if want.Enabled != current {
			on := want.Enabled
			if err := c.Do(ctx, "POST", append(flagEnv, "toggle"),
				p.req(api.Request{Body: api.ToggleRequest{Enabled: &on}, Reason: p.reason(), Replayable: true}), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Plan) ruleRequest(r Rule, varIDs map[string]string) (api.RuleRequest, error) {
	id, ok := varIDs[r.Variation]
	if !ok {
		return api.RuleRequest{}, fmt.Errorf("a rule serves %q, which the flag has no variation for", r.Variation)
	}
	rr := api.RuleRequest{
		Description: optional(r.Description), VariationID: id, RolloutPercentage: r.Rollout,
		ConditionGroups: []api.ConditionGroup{}, BucketBy: optional(r.BucketBy),
	}
	if r.SegmentKey != "" {
		segID, ok := p.segIDs[r.SegmentKey]
		if !ok {
			return api.RuleRequest{}, fmt.Errorf("segment %s was not written", r.SegmentKey)
		}
		rr.UserSegmentID = &segID
		return rr, nil
	}
	for _, g := range r.Groups {
		cg := api.ConditionGroup{Operator: string(g.Operator), Conditions: make([]api.Condition, 0, len(g.Conditions))}
		for _, cd := range g.Conditions {
			cg.Conditions = append(cg.Conditions, api.Condition{
				Attribute: cd.Attribute, Operator: string(cd.Operator), Values: nonNil(cd.Values), Negate: cd.Negate,
			})
		}
		rr.ConditionGroups = append(rr.ConditionGroups, cg)
	}
	return rr, nil
}

// wantEnv is the configuration the import gives f in env. A source with no
// config for env (LaunchDarkly leaves such an environment out) gets the
// flag's default variation, off, with no rules. Writing and comparing both
// read it, so a flag written that way reads as unchanged on the next run.
func wantEnv(f *Flag, env string) *EnvConfig {
	if want := f.Environments[env]; want != nil {
		return want
	}
	return &EnvConfig{Default: defaultVariation(f)}
}

func defaultVariation(f *Flag) string {
	if f.Type == Boolean {
		return "false"
	}
	if len(f.Variations) > 0 {
		return f.Variations[0].Key
	}
	return ""
}

func hasVariation(vs []api.Variation, key string) bool {
	for _, v := range vs {
		if v.Key == key {
			return true
		}
	}
	return false
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
