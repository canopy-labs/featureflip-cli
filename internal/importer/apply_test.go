package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
)

func intp(n int) *int { return &n }

// sampleResult is a translated project covering what the writer maps: a
// segment, a segment-key rule, a rollout rule with OR and AND groups, a JSON
// flag whose default is not its first variation, and per-environment state.
func sampleResult() *Result {
	beta := Segment{Key: "beta", SourceKey: "Beta", Name: "Beta", Description: "testers",
		Conditions: []Condition{
			{Attribute: "email", Operator: OpContains, Values: []string{"@acme.com"}},
			{Attribute: "plan", Operator: OpIn, Values: []string{"pro", "enterprise"}},
		}}
	checkout := Flag{
		Key: "new-checkout", SourceKey: "new_checkout", Name: "New checkout", Type: Boolean,
		Variations: []Variation{{Key: "true", Name: "True", Value: "true"}, {Key: "false", Name: "False", Value: "false"}},
		Environments: map[string]*EnvConfig{
			"dev": {Enabled: true, Default: "false", Rules: []Rule{
				{Description: "Beta", SegmentKey: "beta", Variation: "true"},
				{Description: "Half of EU", Variation: "true", Rollout: intp(50), BucketBy: "user_id", Groups: []ConditionGroup{
					{Operator: Or, Conditions: []Condition{{Attribute: "country", Operator: OpIn, Values: []string{"DE", "FR"}}}},
					{Operator: And, Conditions: []Condition{
						{Attribute: "email", Operator: OpContains, Values: []string{"spam"}, Negate: true},
						{Attribute: "plan", Operator: OpIn, Values: []string{"pro"}},
					}},
				}},
			}},
			"prod": {Enabled: false, Default: "false"},
		},
	}
	theme := Flag{
		Key: "theme", SourceKey: "theme", Name: "Theme", Type: JSON,
		Variations: []Variation{
			{Key: "default", Name: "Default", Value: `{"color":"blue"}`},
			{Key: "dark", Name: "Dark", Value: `{"color":"black"}`},
		},
		Environments: map[string]*EnvConfig{
			"dev":  {Enabled: true, Default: "dark"},
			"prod": {Enabled: true, Default: "default"},
		},
	}
	return &Result{
		Source: "Statsig",
		Project: &Project{
			Environments: []Environment{{Key: "dev", Source: "development"}, {Key: "prod", Source: "production"}},
			Segments:     []Segment{beta},
			Flags:        []Flag{checkout, theme},
		},
		Report: NewReport(),
	}
}

func plan(t *testing.T, ft *fakeTarget, res *Result, opts ApplyOptions) *Plan {
	t.Helper()
	opts.Org, opts.Project = testOrg, testProject
	p, err := BuildPlan(context.Background(), ft.client(), res, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func apply(t *testing.T, ft *fakeTarget, res *Result, opts ApplyOptions) *Plan {
	t.Helper()
	p := plan(t, ft, res, opts)
	if err := p.Apply(context.Background(), ft.client(), nil); err != nil {
		t.Fatal(err)
	}
	return p
}

func actions(p *Plan) map[string]Action {
	out := map[string]Action{}
	for _, st := range p.Steps {
		out[string(st.Kind)+" "+st.Key] = st.Action
	}
	return out
}

func TestApplyWritesTheTranslationAndARerunWritesNothing(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	p := apply(t, ft, sampleResult(), ApplyOptions{})
	for _, st := range p.Steps {
		if st.Action != ActCreate || !st.Done {
			t.Errorf("%s %s: %s done=%v, want a finished create", st.Kind, st.Key, st.Action, st.Done)
		}
	}

	seg := ft.segments["beta"]
	if seg == nil || len(seg.Conditions) != 2 || *seg.Description != "testers" {
		t.Fatalf("segment = %+v", seg)
	}
	fl := ft.flags["new-checkout"]
	dev := fl.Envs["dev"]
	if len(dev.Rules) != 2 {
		t.Fatalf("dev rules = %+v", dev.Rules)
	}
	if dev.Rules[0].UserSegmentID == nil || *dev.Rules[0].UserSegmentID != seg.ID {
		t.Errorf("rule 1 should reference segment %s: %+v", seg.ID, dev.Rules[0])
	}
	r2 := dev.Rules[1]
	if *r2.RolloutPercentage != 50 || len(r2.ConditionGroups) != 2 || r2.ConditionGroups[0].Operator != "Or" || len(r2.ConditionGroups[1].Conditions) != 2 {
		t.Errorf("rule 2 = %+v", r2)
	}
	if varKey(fl, r2.VariationID) != "true" {
		t.Errorf("rule 2 serves %s", varKey(fl, r2.VariationID))
	}
	if dev.Enabled || fl.Envs["prod"].Enabled {
		t.Error("flags must arrive off without --carry-state")
	}
	theme := ft.flags["theme"]
	if varKey(theme, theme.Envs["dev"].Default) != "dark" || varKey(theme, theme.Envs["prod"].Default) != "default" {
		t.Errorf("theme defaults: dev %s, prod %s", varKey(theme, theme.Envs["dev"].Default), varKey(theme, theme.Envs["prod"].Default))
	}

	before := ft.writeCount()
	again := apply(t, ft, sampleResult(), ApplyOptions{})
	for k, a := range actions(again) {
		if a != ActUnchanged {
			t.Errorf("re-run: %s is %s, want unchanged", k, a)
		}
	}
	if ft.writeCount() != before {
		t.Errorf("a re-run wrote: %v", ft.writes[before:])
	}
}

// LaunchDarkly leaves out an environment a flag has no config in. The write
// gives that environment the flag's default; the comparison must expect the
// same, or the flag reads as differing on every re-run.
func TestAFlagWithNoConfigInAnEnvironmentRereadsUnchanged(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	res := sampleResult()
	for i := range res.Project.Flags {
		delete(res.Project.Flags[i].Environments, "prod")
	}
	apply(t, ft, res, ApplyOptions{})

	before := ft.writeCount()
	again := apply(t, ft, res, ApplyOptions{Overwrite: true})
	for k, a := range actions(again) {
		if a != ActUnchanged {
			t.Errorf("re-run: %s is %s, want unchanged", k, a)
		}
	}
	if ft.writeCount() != before {
		t.Errorf("a re-run with --overwrite rewrote: %v", ft.writes[before:])
	}
}

func varKey(fl *fakeFlag, id string) string {
	for _, v := range fl.Variations {
		if v.ID == id {
			return v.Key
		}
	}
	return "?" + id
}

func TestEveryWriteCarriesTheReasonAndCreatesAreIdempotent(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, sampleResult(), ApplyOptions{})
	keys := map[string]string{}
	for _, r := range ft.requests {
		if r.Method == "GET" {
			continue
		}
		if r.Header.Get("X-Intent-Reason") == "" && !strings.HasSuffix(r.URL.Path, "/flags") {
			t.Errorf("%s %s sent no change reason", r.Method, r.URL.Path)
		}
		if r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/toggle") {
			keys[r.URL.Path+" "+r.Header.Get("Idempotency-Key")] = r.Header.Get("Idempotency-Key")
		}
	}
	if len(keys) != 3 {
		t.Fatalf("want 3 creates with distinct keys, got %v", keys)
	}
	for _, k := range keys {
		if len(k) != 36 {
			t.Errorf("idempotency key %q is not a UUID", k)
		}
	}
}

// Someone imports, deletes everything to start over, and imports again within
// the server's 24h idempotency window. The second import must create again,
// not have the server replay the first run's creates of things now gone.
func TestReimportingAfterADeleteCreatesAgain(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, sampleResult(), ApplyOptions{})
	ft.flags = map[string]*fakeFlag{}
	ft.segments = map[string]*api.Segment{}

	p := apply(t, ft, sampleResult(), ApplyOptions{})
	if ft.flags["new-checkout"] == nil || ft.flags["theme"] == nil || ft.segments["beta"] == nil {
		t.Fatalf("re-import after a delete left flags %v, segments %v", sortedKeys(ft.flags), sortedKeys(ft.segments))
	}
	if st := stepFor(p, KindFlag, "new-checkout"); !st.Done {
		t.Fatalf("step = %+v", st)
	}
}

func TestAPrerequisiteOnAnExistingFlagIsADifference(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, sampleResult(), ApplyOptions{})
	ft.flags["theme"].Envs["prod"].Prereqs = []api.Prerequisite{{PrerequisiteFlagKey: "new-checkout", ExpectedVariationKey: "true"}}

	if st := stepFor(plan(t, ft, sampleResult(), ApplyOptions{}), KindFlag, "theme"); st.Action != ActDiffers || strings.Join(st.Environments, ",") != "prod" {
		t.Fatalf("theme = %+v", st)
	}
	apply(t, ft, sampleResult(), ApplyOptions{Overwrite: true})
	if n := len(ft.flags["theme"].Envs["prod"].Prereqs); n != 0 {
		t.Fatalf("--overwrite left %d prerequisites", n)
	}
	if st := stepFor(plan(t, ft, sampleResult(), ApplyOptions{}), KindFlag, "theme"); st.Action != ActUnchanged {
		t.Fatalf("after --overwrite theme = %+v", st)
	}
}

func TestEnvironmentsToConfirmAreThoseWithLiveChanges(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.confirm["prod"] = true
	if got := plan(t, ft, sampleResult(), ApplyOptions{}).EnvironmentsToConfirm(); len(got) != 0 {
		t.Fatalf("creating flags that stay off needs no confirmation, got %v", got)
	}
	if got := plan(t, ft, sampleResult(), ApplyOptions{CarryState: true}).EnvironmentsToConfirm(); strings.Join(got, ",") != "prod" {
		t.Fatalf("theme is turned on in prod: got %v", got)
	}
	ft.confirm["dev"] = true
	apply(t, ft, sampleResult(), ApplyOptions{})
	ft.flags["new-checkout"].Envs["prod"].Rules = []api.TargetingRule{{ID: "x", VariationID: ft.flags["new-checkout"].Variations[0].ID, BucketBy: "user_id", ConditionGroups: []api.ConditionGroup{}}}
	if got := plan(t, ft, sampleResult(), ApplyOptions{Overwrite: true}).EnvironmentsToConfirm(); strings.Join(got, ",") != "prod" {
		t.Fatalf("--overwrite rewrites prod only: got %v", got)
	}
}

func TestOnOffStateOnlyCountsWithCarryState(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, sampleResult(), ApplyOptions{})
	if a := actions(plan(t, ft, sampleResult(), ApplyOptions{})); a["flag theme"] != ActUnchanged {
		t.Fatalf("without --carry-state an off flag matches: %v", a)
	}
	p := plan(t, ft, sampleResult(), ApplyOptions{CarryState: true})
	if st := stepFor(p, KindFlag, "new-checkout"); st.Action != ActDiffers || strings.Join(st.Environments, ",") != "dev" {
		t.Fatalf("new-checkout is on in dev at the source: %+v", st)
	}
}

func TestCarryStateTurnsFlagsOnWhereTheSourceHasThemOn(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, sampleResult(), ApplyOptions{CarryState: true})
	if !ft.flags["new-checkout"].Envs["dev"].Enabled || ft.flags["new-checkout"].Envs["prod"].Enabled {
		t.Error("new-checkout should be on in dev only")
	}
	if !ft.flags["theme"].Envs["prod"].Enabled {
		t.Error("theme should be on in prod")
	}
	before := ft.writeCount()
	apply(t, ft, sampleResult(), ApplyOptions{CarryState: true})
	if ft.writeCount() != before {
		t.Errorf("a re-run with --carry-state wrote: %v", ft.writes[before:])
	}
}

func TestPlanCapStopsBeforeTheFirstWrite(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.plan, ft.flagCap = "Solo", intp(10)
	for i := range 9 {
		ft.addFlag("existing-"+string(rune('a'+i)), "Boolean")
	}
	ft.addFlag("old", "Boolean").Archived = true // archived flags don't count
	p := plan(t, ft, sampleResult(), ApplyOptions{})
	err := p.Apply(context.Background(), ft.client(), nil)
	var lim *LimitError
	if !errors.As(err, &lim) || lim.Needs != 2 || lim.InUse != 9 || lim.Cap != 10 {
		t.Fatalf("err = %v, want a limit error for 2 flags over 9 of 10", err)
	}
	if exitcode.Classify(err) != exitcode.Rejected {
		t.Errorf("exit %d", exitcode.Classify(err))
	}
	if ft.writeCount() != 0 {
		t.Fatalf("wrote before failing: %v", ft.writes)
	}
	if !strings.Contains(err.Error(), "Solo plan allows 10") {
		t.Errorf("message: %v", err)
	}
}

func TestNoCapFromAnOlderServerMeansNoPreflight(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.noLimits = true
	apply(t, ft, sampleResult(), ApplyOptions{})
	if len(ft.flags) != 2 {
		t.Fatalf("flags = %d", len(ft.flags))
	}
}

func TestAnEnvironmentTheProjectLacksIsAnOptionError(t *testing.T) {
	ft := newFakeTarget(t, "dev", "production")
	_, err := BuildPlan(context.Background(), ft.client(), sampleResult(), ApplyOptions{Org: testOrg, Project: testProject})
	var opt OptionError
	if !errors.As(err, &opt) || !strings.Contains(err.Error(), "no environment prod (from production)") ||
		!strings.Contains(err.Error(), "--env-map") || !strings.Contains(err.Error(), "dev, production") {
		t.Fatalf("err = %v", err)
	}
}

func TestADifferingFlagIsLeftAloneUnlessOverwrite(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, sampleResult(), ApplyOptions{})

	// Someone edits prod in Featureflip.
	prod := ft.flags["new-checkout"].Envs["prod"]
	prod.Rules = []api.TargetingRule{{ID: "x", VariationID: ft.flags["new-checkout"].Variations[0].ID, BucketBy: "user_id",
		ConditionGroups: []api.ConditionGroup{{Operator: "And", Conditions: []api.Condition{{Attribute: "user_id", Operator: "In", Values: []string{"me"}}}}}}}

	before := ft.writeCount()
	p := apply(t, ft, sampleResult(), ApplyOptions{})
	st := stepFor(p, KindFlag, "new-checkout")
	if st.Action != ActDiffers || strings.Join(st.Environments, ",") != "prod" {
		t.Fatalf("step = %+v", st)
	}
	if ft.writeCount() != before || len(prod.Rules) != 1 {
		t.Fatalf("a differing flag was written without --overwrite: %v", ft.writes[before:])
	}

	p = apply(t, ft, sampleResult(), ApplyOptions{Overwrite: true})
	if st := stepFor(p, KindFlag, "new-checkout"); st.Action != ActUpdate || !st.Done {
		t.Fatalf("step = %+v", st)
	}
	if len(ft.flags["new-checkout"].Envs["prod"].Rules) != 0 {
		t.Fatal("--overwrite should have replaced prod's rules")
	}
	for _, w := range ft.writes[before:] {
		if strings.Contains(w, "/environments/dev") {
			t.Errorf("--overwrite rewrote an environment that matched: %s", w)
		}
	}
	for k, a := range actions(plan(t, ft, sampleResult(), ApplyOptions{})) {
		if a != ActUnchanged {
			t.Errorf("after --overwrite, %s is %s", k, a)
		}
	}
}

func TestADifferingSegmentBlocksTheFlagsThatUseIt(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.segments["beta"] = &api.Segment{ID: "seg-x", Key: "beta", Name: "Beta",
		Conditions: []api.SegmentCondition{{Attribute: "email", Operator: "Contains", Values: []string{"@other.com"}}}}

	p := apply(t, ft, sampleResult(), ApplyOptions{})
	a := actions(p)
	if a["segment beta"] != ActDiffers || a["flag new-checkout"] != ActBlocked || a["flag theme"] != ActCreate {
		t.Fatalf("actions = %v", a)
	}
	if !strings.Contains(stepFor(p, KindFlag, "new-checkout").Why, "segment beta") {
		t.Errorf("why = %q", stepFor(p, KindFlag, "new-checkout").Why)
	}
	if ft.flags["new-checkout"] != nil || ft.segments["beta"].Conditions[0].Values[0] != "@other.com" {
		t.Fatal("wrote the flag or the segment")
	}

	p = apply(t, ft, sampleResult(), ApplyOptions{Overwrite: true})
	if a := actions(p); a["segment beta"] != ActUpdate || a["flag new-checkout"] != ActCreate {
		t.Fatalf("with --overwrite: %v", a)
	}
	if *ft.flags["new-checkout"].Envs["dev"].Rules[0].UserSegmentID != "seg-x" {
		t.Error("the rule should reference the updated segment")
	}
}

func TestFlagsTheImportCantUpdateAreConflicts(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.addFlag("new-checkout", "Boolean").Archived = true
	ft.addFlag("theme", "Json", api.Variation{Key: "default", Value: `{"color":"red"}`}, api.Variation{Key: "dark", Value: `{"color":"black"}`})

	p := apply(t, ft, sampleResult(), ApplyOptions{Overwrite: true})
	for _, key := range []string{"new-checkout", "theme"} {
		if st := stepFor(p, KindFlag, key); st.Action != ActConflict {
			t.Errorf("%s: %+v", key, st)
		}
	}
	for _, w := range ft.writes {
		if strings.Contains(w, "/flags") {
			t.Errorf("wrote a conflicting flag: %s", w)
		}
	}
}

func TestEquivalentJSONValuesAreTheSameVariation(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.addFlag("theme", "Json", api.Variation{Key: "default", Value: `{ "color": "blue" }`}, api.Variation{Key: "dark", Value: `{"color":"black"}`})
	p := plan(t, ft, sampleResult(), ApplyOptions{})
	if st := stepFor(p, KindFlag, "theme"); st.Action != ActDiffers {
		t.Fatalf("theme: %+v, want differs (its defaults differ, its variations match)", st)
	}
}

func TestReportBlockedItemsAreNeverWritten(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	res := sampleResult()
	res.Report.Add(Error, KindFlag, "theme", "x", "untranslatable", "")
	p := apply(t, ft, res, ApplyOptions{})
	if stepFor(p, KindFlag, "theme").Action != ActBlocked || ft.flags["theme"] != nil {
		t.Fatal("a blocked flag was written")
	}
}

func TestAFailedWriteStopsAndSaysWhere(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.failOn = "/flags/new-checkout/environments/dev/targeting"
	p := plan(t, ft, sampleResult(), ApplyOptions{})
	err := p.Apply(context.Background(), ft.client(), nil)
	var ae *ApplyError
	if !errors.As(err, &ae) || ae.Step.Key != "new-checkout" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "environment dev") || !strings.Contains(err.Error(), "--overwrite") {
		t.Errorf("message: %v", err)
	}
	if !stepFor(p, KindSegment, "beta").Done || stepFor(p, KindFlag, "new-checkout").Done || ft.flags["theme"] != nil {
		t.Error("the import should stop at the failed flag")
	}
	if !stepFor(p, KindFlag, "new-checkout").Created || ae.Plan != p {
		t.Error("the error should carry the plan, with the flag recorded as created")
	}

	// The summary says the flag exists, part configured, and how to finish it.
	var out bytes.Buffer
	if err := sampleResult().RenderApplied(&out, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Flags     1 created but not fully configured (stopped at an error), 1 not written (stopped at an error)",
		"flag new-checkout was created but not fully configured. Re-run the import with --overwrite",
		"Imported flags are off in every environment",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// The re-run finishes it with --overwrite.
	apply(t, ft, sampleResult(), ApplyOptions{Overwrite: true})
	for k, a := range actions(plan(t, ft, sampleResult(), ApplyOptions{})) {
		if a != ActUnchanged {
			t.Errorf("after finishing, %s is %s", k, a)
		}
	}
}

func TestApplyRendering(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.addFlag("theme", "Json", api.Variation{Key: "default", Value: `{"color":"red"}`}, api.Variation{Key: "dark", Value: `{"color":"black"}`})
	res := sampleResult()
	p := apply(t, ft, res, ApplyOptions{})

	var out bytes.Buffer
	if err := res.RenderApplied(&out, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Statsig import into acme/web.",
		"Segments  1 created",
		"Flags     1 created, 1 left untouched",
		"flag theme: a flag with this key exists in Featureflip with other variations",
		"--carry-state",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	raw, _ := json.Marshal(res.ApplyDocument(p))
	var doc struct {
		DryRun bool `json:"dryRun"`
		Target struct {
			Org   string `json:"org"`
			Steps []struct {
				Kind, Key, Action string
				Done              bool
			} `json:"steps"`
		} `json:"target"`
	}
	json.Unmarshal(raw, &doc)
	if doc.DryRun || doc.Target.Org != "acme" || len(doc.Target.Steps) != 3 || doc.Target.Steps[0].Action != "create" || !doc.Target.Steps[0].Done {
		t.Errorf("json: %s", raw)
	}
}

func stepFor(p *Plan, kind Kind, key string) *Step {
	for _, st := range p.Steps {
		if st.Kind == kind && st.Key == key {
			return st
		}
	}
	return nil
}

func TestAFlagMarkedClientSideIsCreatedClientSide(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	res := sampleResult()
	res.Project.Flags[0].ClientSide = true
	apply(t, ft, res, ApplyOptions{})
	if !ft.flags["new-checkout"].ClientSide {
		t.Fatal("new-checkout was created server-side only")
	}
	if ft.flags["theme"].ClientSide {
		t.Fatal("theme was created client-side without being marked")
	}
}

// chainResult is three flags in a prerequisite chain, listed dependents first:
// dependent needs parent=true in prod, parent needs root=on everywhere.
func chainResult() *Result {
	boolVars := []Variation{{Key: "true", Name: "True", Value: "true"}, {Key: "false", Name: "False", Value: "false"}}
	dependent := Flag{Key: "dependent", SourceKey: "dependent", Name: "Dependent", Type: Boolean, Variations: boolVars,
		Environments: map[string]*EnvConfig{
			"dev":  {Default: "false"},
			"prod": {Default: "false", Prerequisites: []Prerequisite{{Flag: "parent", Variation: "true"}}},
		}}
	parent := Flag{Key: "parent", SourceKey: "parent", Name: "Parent", Type: Boolean, Variations: boolVars,
		Environments: map[string]*EnvConfig{
			"dev":  {Default: "true", Prerequisites: []Prerequisite{{Flag: "root", Variation: "on"}}},
			"prod": {Default: "true", Prerequisites: []Prerequisite{{Flag: "root", Variation: "on"}}},
		}}
	root := Flag{Key: "root", SourceKey: "root", Name: "Root", Type: String,
		Variations:   []Variation{{Key: "on", Name: "On", Value: "on"}, {Key: "off", Name: "Off", Value: "off"}},
		Environments: map[string]*EnvConfig{"dev": {Default: "off"}, "prod": {Default: "on"}}}
	return &Result{
		Source: "Test",
		Project: &Project{
			Environments: []Environment{{Key: "dev", Source: "dev"}, {Key: "prod", Source: "prod"}},
			Segments:     []Segment{},
			Flags:        []Flag{dependent, parent, root},
		},
		Report: NewReport(),
	}
}

func TestPrerequisitesAreWrittenInDependencyOrder(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, chainResult(), ApplyOptions{})
	got := ft.flags["dependent"].Envs["prod"].Prereqs
	if len(got) != 1 || got[0].PrerequisiteFlagKey != "parent" || got[0].ExpectedVariationKey != "true" {
		t.Fatalf("dependent prod prerequisites = %+v", got)
	}
	var created []string
	for _, w := range ft.writes {
		if strings.HasSuffix(w, "/projects/"+testProject+"/flags") {
			created = append(created, w)
		}
	}
	if len(created) != 3 {
		t.Fatalf("creates = %v", created)
	}
	var order []string
	for _, st := range plan(t, newFakeTarget(t, "dev", "prod"), chainResult(), ApplyOptions{}).Steps {
		order = append(order, st.Key)
	}
	if strings.Join(order, ",") != "root,parent,dependent" {
		t.Fatalf("step order = %v", order)
	}
}

func TestPrerequisitesRoundTripUnchanged(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, chainResult(), ApplyOptions{})
	p := plan(t, ft, chainResult(), ApplyOptions{})
	for _, st := range p.Steps {
		if st.Action != ActUnchanged {
			t.Fatalf("%s = %s (%s) on a re-run", st.Key, st.Action, st.Environments)
		}
	}
	n := ft.writeCount()
	apply(t, ft, chainResult(), ApplyOptions{})
	if ft.writeCount() != n {
		t.Fatalf("a re-run wrote %d times", ft.writeCount()-n)
	}
}

func TestAPrerequisiteThatChangedIsADifference(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, chainResult(), ApplyOptions{})
	ft.flags["dependent"].Envs["prod"].Prereqs = nil
	st := stepFor(plan(t, ft, chainResult(), ApplyOptions{}), KindFlag, "dependent")
	if st.Action != ActDiffers || strings.Join(st.Environments, ",") != "prod" {
		t.Fatalf("dependent = %+v", st)
	}
	apply(t, ft, chainResult(), ApplyOptions{Overwrite: true})
	if got := ft.flags["dependent"].Envs["prod"].Prereqs; len(got) != 1 {
		t.Fatalf("--overwrite left prerequisites %+v", got)
	}
}

func TestABlockedPrerequisiteBlocksItsDependents(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	res := chainResult()
	res.Report.Add(Error, KindFlag, "root", "test", "blocked for the test", "")
	p := plan(t, ft, res, ApplyOptions{})
	for _, key := range []string{"parent", "dependent"} {
		st := stepFor(p, KindFlag, key)
		if st.Action != ActBlocked || !strings.Contains(st.Why, "prerequisite") {
			t.Fatalf("%s = %+v", key, st)
		}
	}
	if !strings.Contains(stepFor(p, KindFlag, "parent").Why, "root") {
		t.Fatalf("parent why = %q", stepFor(p, KindFlag, "parent").Why)
	}
}

func TestAConflictingPrerequisiteBlocksItsDependents(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.addFlag("root", "String", api.Variation{Key: "x", Value: "x"}, api.Variation{Key: "y", Value: "y"})
	p := plan(t, ft, chainResult(), ApplyOptions{})
	if a := stepFor(p, KindFlag, "root").Action; a != ActConflict {
		t.Fatalf("root = %s", a)
	}
	for _, key := range []string{"parent", "dependent"} {
		if st := stepFor(p, KindFlag, key); st.Action != ActBlocked {
			t.Fatalf("%s = %+v", key, st)
		}
	}
}

func TestABlockedPrerequisiteCycleDoesntStopThePlan(t *testing.T) {
	res := chainResult()
	// root needs dependent: a cycle the translation reported and blocked.
	res.Project.Flags[2].Environments["prod"].Prerequisites = []Prerequisite{{Flag: "dependent", Variation: "true"}}
	for _, key := range []string{"root", "parent", "dependent"} {
		res.Report.Add(Error, KindFlag, key, "prerequisite-cycle", "a cycle", "")
	}
	p := plan(t, newFakeTarget(t, "dev", "prod"), res, ApplyOptions{})
	for _, key := range []string{"root", "parent", "dependent"} {
		if a := stepFor(p, KindFlag, key).Action; a != ActBlocked {
			t.Fatalf("%s = %s", key, a)
		}
	}
}

func TestAFrozenWriteStopsTheImportBeforeAnythingElseIsWritten(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.frozen = true
	p := plan(t, ft, sampleResult(), ApplyOptions{})
	err := p.Apply(context.Background(), ft.client(), nil)
	var ae *ApplyError
	var apiErr *api.Error
	if !errors.As(err, &ae) || ae.Step.Kind != KindSegment || !errors.As(err, &apiErr) ||
		apiErr.Code != "change_frozen" || apiErr.ChangeFreeze == nil || apiErr.ChangeFreeze.EnvironmentKey != "prod" {
		t.Fatalf("err = %v", err)
	}
	if err.(*ApplyError).ExitCode() != exitcode.Frozen {
		t.Errorf("exit code = %d", err.(*ApplyError).ExitCode())
	}
	if len(ft.writes) != 1 || len(ft.flags) != 0 {
		t.Errorf("nothing after the frozen segment should be written: %v", ft.writes)
	}
}

func TestBreakFreezeIsSentOnEveryWrite(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	ft.frozen = true
	apply(t, ft, sampleResult(), ApplyOptions{BreakFreeze: true, CarryState: true, Reason: "import"})
	writes := 0
	for _, r := range ft.requests {
		if r.Method == "GET" {
			continue
		}
		writes++
		if r.Header.Get("X-Freeze-Override") != "true" {
			t.Errorf("%s %s sent no freeze override", r.Method, r.URL.Path)
		}
	}
	if writes == 0 || len(ft.flags) != 2 {
		t.Errorf("the import should complete: %d writes, %d flags", writes, len(ft.flags))
	}
}

func TestNoFreezeOverrideHeaderWithoutBreakFreeze(t *testing.T) {
	ft := newFakeTarget(t, "dev", "prod")
	apply(t, ft, sampleResult(), ApplyOptions{})
	for _, r := range ft.requests {
		if r.Header.Get("X-Freeze-Override") != "" {
			t.Errorf("%s %s sent a freeze override", r.Method, r.URL.Path)
		}
	}
}
