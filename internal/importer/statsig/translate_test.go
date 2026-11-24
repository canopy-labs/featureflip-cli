package statsig

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

func loadSnapshot(t *testing.T) *snapshot {
	t.Helper()
	b, err := os.ReadFile("testdata/project.json")
	if err != nil {
		t.Fatal(err)
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func translated(t *testing.T) *im.Result {
	t.Helper()
	res, err := translate(loadSnapshot(t), im.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res.Summarize()
	return res
}

func flag(t *testing.T, res *im.Result, source string) im.Flag {
	t.Helper()
	for _, f := range res.Project.Flags {
		if f.SourceKey == source {
			return f
		}
	}
	t.Fatalf("no flag for %q", source)
	return im.Flag{}
}

func segmentOf(t *testing.T, res *im.Result, source string) im.Segment {
	t.Helper()
	for _, s := range res.Project.Segments {
		if s.SourceKey == source {
			return s
		}
	}
	t.Fatalf("no segment for %q", source)
	return im.Segment{}
}

func issue(res *im.Result, subject, code string) *im.Issue {
	for _, is := range res.Report.Issues() {
		if is.Subject == subject && is.Code == code {
			return &is
		}
	}
	return nil
}

func mustIssue(t *testing.T, res *im.Result, subject, code string, sev im.Severity) im.Issue {
	t.Helper()
	is := issue(res, subject, code)
	if is == nil {
		t.Fatalf("no %s issue for %q; issues: %+v", code, subject, res.Report.Issues())
	}
	if is.Severity != sev {
		t.Fatalf("%s issue for %q is %s, want %s", code, subject, is.Severity, sev)
	}
	return *is
}

func and(cs ...im.Condition) im.ConditionGroup {
	return im.ConditionGroup{Operator: im.And, Conditions: cs}
}

func in(attr string, values ...string) im.Condition {
	return im.Condition{Attribute: attr, Operator: im.OpIn, Values: values}
}

func intp(n int) *int { return &n }

func TestEnvironmentsKeepTheirNamesUnlessMapped(t *testing.T) {
	res, err := translate(loadSnapshot(t), im.Options{EnvMap: map[string]string{"production": "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []im.Environment{{Key: "development", Source: "development"}, {Key: "staging", Source: "staging"}, {Key: "prod", Source: "production"}}
	if !reflect.DeepEqual(res.Project.Environments, want) {
		t.Fatalf("environments = %+v", res.Project.Environments)
	}
	if _, ok := flag(t, res, "public_gate").Environments["prod"]; !ok {
		t.Fatal("flag configs should be keyed by the mapped environment")
	}
}

func TestEnvMapErrorsAreUsageErrors(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"unknown source": {"qa": "qa"},
		"two onto one":   {"development": "prod", "production": "prod"},
	} {
		_, err := translate(loadSnapshot(t), im.Options{EnvMap: m})
		if err == nil || exitcode.Classify(err) != exitcode.Usage {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
	var opt im.OptionError
	_, err := translate(loadSnapshot(t), im.Options{EnvMap: map[string]string{"qa": "qa"}})
	if !errors.As(err, &opt) || !strings.Contains(err.Error(), "development, staging, production") {
		t.Fatalf("err = %v, want it to list the project's environments", err)
	}
}

func TestPublicRuleServesTrueEverywhere(t *testing.T) {
	f := flag(t, translated(t), "public_gate")
	if f.Type != im.Boolean || f.Key != "public_gate" || f.Name != "Public gate" || !reflect.DeepEqual(f.Tags, []string{"checkout"}) {
		t.Fatalf("flag = %+v", f)
	}
	for _, env := range []string{"development", "staging", "production"} {
		ec := f.Environments[env]
		want := []im.Rule{{Description: "Everyone", Groups: []im.ConditionGroup{}, Variation: "true"}}
		if !ec.Enabled || ec.Default != "false" || !reflect.DeepEqual(ec.Rules, want) {
			t.Fatalf("%s = %+v", env, ec)
		}
	}
}

// A Statsig rule is terminal: units outside the pass percentage get false.
// That is a Featureflip rollout serving true, with the rest on false.
func TestPassPercentages(t *testing.T) {
	res := translated(t)
	rules := flag(t, res, "rollout_gate").Environments["production"].Rules
	want := []im.Rule{
		{Description: "Thirty", Groups: []im.ConditionGroup{and(in("email", "a@x.com"))}, Variation: "true", Rollout: intp(30), BucketBy: "user_id"},
		{Description: "Fraction", Groups: []im.ConditionGroup{and(in("user_id", "u9"))}, Variation: "true", Rollout: intp(33), BucketBy: "user_id"},
		{Description: "Zero", Groups: []im.ConditionGroup{}, Variation: "false"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules =\n%+v\nwant\n%+v", rules, want)
	}
	mustIssue(t, res, "rollout_gate", "rollout-rounded", im.Warning)
	mustIssue(t, res, "rollout_gate", "scheduled-rollout", im.Warning)
}

func TestRuleEnvironmentsAndEnvironmentTier(t *testing.T) {
	f := flag(t, translated(t), "env_gate")
	names := func(env string) []string {
		var out []string
		for _, r := range f.Environments[env].Rules {
			out = append(out, r.Description)
		}
		return out
	}
	if got := names("development"); got != nil {
		t.Errorf("development rules = %v, want none", got)
	}
	if got := names("staging"); !reflect.DeepEqual(got, []string{"Staging tier"}) {
		t.Errorf("staging rules = %v", got)
	}
	if got := names("production"); !reflect.DeepEqual(got, []string{"Prod only"}) {
		t.Errorf("production rules = %v", got)
	}
}

func TestGateChecksBlockTheFlag(t *testing.T) {
	res := translated(t)
	if !flag(t, res, "gate_on_gate").Blocked {
		t.Fatal("a rule checking another gate has no Featureflip equivalent; the flag must be blocked")
	}
	is := mustIssue(t, res, "gate_on_gate", "unsupported-condition", im.Error)
	if !strings.Contains(is.Message, "passes_gate") {
		t.Fatalf("message = %q", is.Message)
	}
}

func TestSegments(t *testing.T) {
	res := translated(t)

	beta := segmentOf(t, res, "beta_testers")
	if beta.Blocked || beta.Key != "beta_testers" || !reflect.DeepEqual(beta.Conditions,
		[]im.Condition{{Attribute: "email", Operator: im.OpContains, Values: []string{"@acme.com"}}}) {
		t.Fatalf("beta = %+v", beta)
	}
	vip := segmentOf(t, res, "vip")
	if vip.Blocked || !reflect.DeepEqual(vip.Conditions, []im.Condition{in("user_id", "u1", "u2")}) {
		t.Fatalf("vip = %+v", vip)
	}
	internal := segmentOf(t, res, "Internal Or")
	if !internal.Blocked || internal.Key != "internal-or" {
		t.Fatalf("a two-rule segment can't be one Featureflip segment: %+v", internal)
	}
	mustIssue(t, res, "Internal Or", "segment-not-representable", im.Error)
	mustIssue(t, res, "partial", "segment-partial-rule", im.Error)
	mustIssue(t, res, "analysis", "segment-type", im.Error)
}

func TestSegmentRules(t *testing.T) {
	res := translated(t)
	rules := flag(t, res, "segment_gate").Environments["production"].Rules
	email := im.Condition{Attribute: "email", Operator: im.OpContains, Values: []string{"@acme.com"}}
	want := []im.Rule{
		// The rule's only condition names a segment that translates: a segment rule.
		{Description: "Beta", Groups: []im.ConditionGroup{}, SegmentKey: "beta_testers", Variation: "true"},
		// With another condition beside it, the segment's conditions are inlined,
		// because the API ignores condition groups on a segment rule.
		{Description: "Beta in US", Groups: []im.ConditionGroup{and(email, in("region", "us"))}, Variation: "true"},
		// A segment of single-condition rules becomes one OR group.
		{Description: "Internal", Groups: []im.ConditionGroup{{Operator: im.Or, Conditions: []im.Condition{in("country", "US"), in("plan", "pro")}}}, Variation: "true"},
		// Failing a segment negates each of its rules.
		{Description: "Not VIP", Groups: []im.ConditionGroup{{Operator: im.Or, Conditions: []im.Condition{{Attribute: "user_id", Operator: im.OpIn, Values: []string{"u1", "u2"}, Negate: true}}}}, Variation: "true"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules =\n%+v\nwant\n%+v", rules, want)
	}
	if flag(t, res, "segment_gate").Blocked {
		t.Fatal("segment_gate translates fully")
	}
}

func TestMultiConditionSegmentRulesExpand(t *testing.T) {
	res := translated(t)
	rules := flag(t, res, "expanded_gate").Environments["staging"].Rules
	// "none" is a negated In, never NotIn: a context with no user_id passes
	// "none" in Statsig, and only a negated condition passes on a missing
	// attribute in Featureflip.
	notX := im.Condition{Attribute: "user_id", Operator: im.OpIn, Values: []string{"x"}, Negate: true}
	want := []im.Rule{
		{Description: "Multi", Groups: []im.ConditionGroup{and(notX, in("plan", "pro"),
			im.Condition{Attribute: "seats", Operator: im.OpGreaterThan, Values: []string{"10"}})}, Variation: "true"},
		{Description: "Multi", Groups: []im.ConditionGroup{and(notX, in("plan", "enterprise"), in("locale", "en-US"))}, Variation: "true"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules =\n%+v\nwant\n%+v", rules, want)
	}
	mustIssue(t, res, "expanded_gate", "segment-inlined", im.Info)
}

func TestUnusableSegmentBlocksTheFlag(t *testing.T) {
	res := translated(t)
	if !flag(t, res, "partial_seg_gate").Blocked {
		t.Fatal("a rule using an untranslatable segment must block the flag")
	}
	mustIssue(t, res, "partial_seg_gate", "segment-unusable", im.Error)
}

func TestLaunchedAndDisabledGates(t *testing.T) {
	res := translated(t)
	// The Console API doesn't say what a launched gate serves, so it is
	// blocked rather than guessed.
	if !flag(t, res, "Launched Gate").Blocked {
		t.Fatal("a launched gate must be blocked")
	}
	mustIssue(t, res, "Launched Gate", "launched", im.Error)
	mustIssue(t, res, "Launched Gate", "key-renamed", im.Warning)

	// Status Disabled wins over isEnabled: Statsig serves false.
	disabled := flag(t, res, "disabled_gate").Environments["production"]
	if disabled.Enabled || len(disabled.Rules) != 1 {
		t.Fatalf("a disabled gate is off, with its rules kept: %+v", disabled)
	}
}

func TestArchivedItemsAreNoted(t *testing.T) {
	res := translated(t)
	for _, f := range res.Project.Flags {
		if f.SourceKey == "old_gate" {
			t.Fatal("an archived gate is not imported")
		}
	}
	is := mustIssue(t, res, "", "archived", im.Info)
	if !strings.Contains(is.Message, "old_gate") {
		t.Fatalf("message = %q", is.Message)
	}
}

func TestOverridesLeadTheRules(t *testing.T) {
	res := translated(t)
	f := flag(t, res, "override_gate")
	prod := f.Environments["production"].Rules
	// The production-only override names the environment by its id.
	if len(prod) != 2 ||
		!reflect.DeepEqual(prod[0].Groups, []im.ConditionGroup{and(in("user_id", "a", "both", "prod-only"))}) || prod[0].Variation != "true" ||
		!reflect.DeepEqual(prod[1].Groups, []im.ConditionGroup{and(in("user_id", "b", "both"))}) || prod[1].Variation != "false" {
		t.Fatalf("production rules = %+v", prod)
	}
	staging := f.Environments["staging"].Rules
	if len(staging) != 3 || !reflect.DeepEqual(staging[0].Groups, []im.ConditionGroup{and(in("stableID", "s1"))}) ||
		!reflect.DeepEqual(staging[1].Groups, []im.ConditionGroup{and(in("user_id", "a", "both"))}) {
		t.Fatalf("staging rules = %+v", staging)
	}
	mustIssue(t, res, "override_gate", "override-conflict", im.Warning)
	mustIssue(t, res, "override_gate", "id-case", im.Info)
}

func TestSegmentProblemsReachTheFlagsThatUseThem(t *testing.T) {
	res := translated(t)
	// Two segments normalizing to one key: neither can be pointed at.
	for _, src := range []string{"Beta Users", "beta-users"} {
		mustIssue(t, res, src, "key-collision", im.Error)
	}
	if !flag(t, res, "collide_seg_gate").Blocked {
		t.Fatal("a rule using a segment whose key collides must block the flag")
	}
	// A segment rule scoped to one environment can't be one Featureflip segment.
	mustIssue(t, res, "prod_only_seg", "segment-env-scoped", im.Error)
	if !flag(t, res, "env_seg_gate").Blocked {
		t.Fatal("a rule using an environment-scoped segment must block the flag")
	}
	// The inlined segment's warnings are repeated on the flag.
	is := mustIssue(t, res, "segment_gate", "derived-attribute", im.Warning)
	if !strings.Contains(is.Message, `"Internal Or"`) {
		t.Fatalf("message = %q", is.Message)
	}
	if !res.Report.Has(im.KindFlag, "segment_gate", im.Warning) {
		t.Fatal("segment_gate should count as warned")
	}
}

func TestRolloutsWithLaterRulesWarn(t *testing.T) {
	res := translated(t)
	is := mustIssue(t, res, "rollout_gate", "unbucketable-falls-through", im.Warning)
	if !strings.Contains(is.Message, `"Thirty"`) {
		t.Fatalf("message = %q", is.Message)
	}
	// The last rule has nothing after it.
	if issue(res, "stable_gate", "unbucketable-falls-through") != nil {
		t.Fatal("a rollout that is the last rule has nothing to fall through to")
	}
}

func TestConditionTranslation(t *testing.T) {
	res := translated(t)
	rules := flag(t, res, "fancy_conditions").Environments["production"].Rules
	want := []im.ConditionGroup{and(
		in("country", "US"),
		in("plan", "Pro"),
		in("companyID", "c1"),
		im.Condition{Attribute: "signup", Operator: im.OpAfter, Values: []string{"2023-11-14T22:13:20Z"}},
		im.Condition{Attribute: "appVersion", Operator: im.OpSemverGreaterEq, Values: []string{"1.2"}},
	)}
	if len(rules) != 1 || !reflect.DeepEqual(rules[0].Groups, want) {
		t.Fatalf("groups =\n%+v\nwant\n%+v", rules[0].Groups, want)
	}
	for _, code := range []string{"derived-attribute", "case-insensitive", "date-attribute", "version-format"} {
		mustIssue(t, res, "fancy_conditions", code, im.Warning)
	}
}

func TestKeyCollisionsBlockBothFlags(t *testing.T) {
	res := translated(t)
	for _, src := range []string{"Dup Key", "dup-key"} {
		if !flag(t, res, src).Blocked {
			t.Errorf("%q should be blocked: another gate normalizes to the same key", src)
		}
	}
}

func TestTemplatesAreSkipped(t *testing.T) {
	res := translated(t)
	for _, f := range res.Project.Flags {
		if f.SourceKey == "a_template" {
			t.Fatal("a template gate is not a flag")
		}
	}
}

func TestUnitTypeAndScopingWarnings(t *testing.T) {
	res := translated(t)
	r := flag(t, res, "stable_gate").Environments["production"].Rules[0]
	if r.BucketBy != "stableID" || *r.Rollout != 50 {
		t.Fatalf("rule = %+v", r)
	}
	mustIssue(t, res, "stable_gate", "unit-attribute", im.Info)
	mustIssue(t, res, "stable_gate", "target-apps", im.Warning)
	mustIssue(t, res, "stable_gate", "holdouts", im.Warning)
}

func TestDynamicConfigs(t *testing.T) {
	res := translated(t)

	two := flag(t, res, "two_value")
	if two.Type != im.JSON || !reflect.DeepEqual(two.Variations, []im.Variation{
		{Key: "default", Name: "Default", Value: `{"a":1}`},
		{Key: "on", Name: "On", Value: `{"a":2}`},
	}) {
		t.Fatalf("variations = %+v", two.Variations)
	}
	ec := two.Environments["production"]
	if ec.Default != "default" || len(ec.Rules) != 1 || ec.Rules[0].Variation != "on" || *ec.Rules[0].Rollout != 25 {
		t.Fatalf("config = %+v", ec)
	}

	// A partial rollout with three values would spread the rest over two of them.
	if !flag(t, res, "three_value").Blocked {
		t.Fatal("three_value should be blocked")
	}
	mustIssue(t, res, "three_value", "partial-rollout-multi-value", im.Error)

	// Equal values (key order aside) share a variation, and big integers stay exact.
	dedupe := flag(t, res, "dedupe")
	if len(dedupe.Variations) != 1 || dedupe.Variations[0].Value != `{"big":12345678901234567890,"x":1}` {
		t.Fatalf("variations = %+v", dedupe.Variations)
	}
	if v := dedupe.Environments["production"].Rules[0].Variation; v != "default" {
		t.Fatalf("rule serves %q", v)
	}
	mustIssue(t, res, "dedupe", "schema", im.Info)

	mustIssue(t, res, "variant_cfg", "variants", im.Error)
	if flag(t, res, "disabled_cfg").Environments["production"].Enabled {
		t.Fatal("a disabled config is off")
	}
}

func TestEveryReadyFlagIsWellFormed(t *testing.T) {
	res := translated(t)
	for _, f := range res.Project.Flags {
		if f.Blocked {
			continue
		}
		keys := map[string]bool{}
		for _, v := range f.Variations {
			if keys[v.Key] {
				t.Errorf("%s: two variations share the key %q", f.SourceKey, v.Key)
			}
			keys[v.Key] = true
			if !json.Valid([]byte(v.Value)) {
				t.Errorf("%s: variation %q is not JSON", f.SourceKey, v.Value)
			}
		}
		if len(f.Environments) != 3 {
			t.Errorf("%s has %d environments", f.SourceKey, len(f.Environments))
		}
		for env, ec := range f.Environments {
			if !keys[ec.Default] {
				t.Errorf("%s/%s: default %q is not a variation", f.SourceKey, env, ec.Default)
			}
			for _, r := range ec.Rules {
				if !keys[r.Variation] {
					t.Errorf("%s/%s: rule serves %q, not a variation", f.SourceKey, env, r.Variation)
				}
				if r.SegmentKey != "" && len(r.Groups) > 0 {
					t.Errorf("%s/%s: a segment rule carries condition groups the API would ignore", f.SourceKey, env)
				}
				if r.Rollout != nil && (*r.Rollout < 1 || *r.Rollout > 99) {
					t.Errorf("%s/%s: rollout %d", f.SourceKey, env, *r.Rollout)
				}
			}
		}
	}
}
