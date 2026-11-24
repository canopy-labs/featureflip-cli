package launchdarkly

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

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

func flagOf(t *testing.T, res *im.Result, source string) im.Flag {
	t.Helper()
	for _, f := range res.Project.Flags {
		if f.SourceKey == source {
			return f
		}
	}
	t.Fatalf("no flag for %q", source)
	return im.Flag{}
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

func present(attr string) im.Condition {
	return im.Condition{Attribute: attr, Operator: im.OpMatchesRegex, Values: []string{"^"}}
}

func intp(n int) *int { return &n }

func TestEnvironmentsKeepTheirKeysUnlessMapped(t *testing.T) {
	res := translated(t)
	if got := res.Project.Environments; len(got) != 2 || got[0].Key != "test" || got[1].Key != "production" {
		t.Fatalf("environments = %+v", got)
	}
	mapped, err := translate(loadSnapshot(t), im.Options{EnvMap: map[string]string{"production": "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := mapped.Project.Environments[1]; got.Key != "prod" || got.Source != "production" {
		t.Fatalf("mapped = %+v", got)
	}
	if _, ok := flagOf(t, mapped, "bool-flag").Environments["prod"]; !ok {
		t.Fatal("bool-flag has no prod configuration")
	}
	_, err = translate(loadSnapshot(t), im.Options{EnvMap: map[string]string{"staging": "stage"}})
	var opt im.OptionError
	if !errors.As(err, &opt) {
		t.Fatalf("unknown --env-map name: err = %v", err)
	}
}

func TestFlagTypesAndVariationKeys(t *testing.T) {
	res := translated(t)
	keysOf := func(f im.Flag) (keys, values []string) {
		for _, v := range f.Variations {
			keys, values = append(keys, v.Key), append(values, v.Value)
		}
		return
	}
	cases := []struct {
		flag   string
		typ    im.FlagType
		keys   []string
		values []string
	}{
		{"bool-flag", im.Boolean, []string{"true", "false"}, []string{"true", "false"}},
		{"color", im.String, []string{"blue-sky", "red", "primary"}, []string{"Blue Sky", "red", "green"}},
		{"limit", im.Number, []string{"variation-1", "variation-2"}, []string{"1", "2.5"}},
		{"config", im.JSON, []string{"variation-1", "variation-2"}, []string{`{"a":2,"b":1}`, `{"x":[1]}`}},
		{"dupe-names", im.String, []string{"a", "a-2"}, []string{"x", "y"}},
	}
	for _, c := range cases {
		f := flagOf(t, res, c.flag)
		keys, values := keysOf(f)
		if f.Type != c.typ || !reflect.DeepEqual(keys, c.keys) || !reflect.DeepEqual(values, c.values) {
			t.Errorf("%s: %s %v %v, want %s %v %v", c.flag, f.Type, keys, values, c.typ, c.keys, c.values)
		}
	}
	if f := flagOf(t, res, "color"); f.Name != "Color" || f.Description != "Theme color" {
		t.Errorf("color name/description = %q/%q", f.Name, f.Description)
	}
	if f := flagOf(t, res, "bool-flag"); !reflect.DeepEqual(f.Tags, []string{"web"}) {
		t.Errorf("bool-flag tags = %v", f.Tags)
	}
}

func TestFlagSeenInEveryEnvironmentIsNotACollision(t *testing.T) {
	res := translated(t)
	for _, is := range res.Report.Issues() {
		if is.Code == "key-collision" && is.Subject != "a-b" && is.Subject != "A-B" {
			t.Fatalf("unexpected collision: %+v", is)
		}
	}
}

func TestKeyRenamedAndCollision(t *testing.T) {
	res := translated(t)
	if f := flagOf(t, res, "My.Flag"); f.Key != "my-flag" {
		t.Fatalf("My.Flag key = %q", f.Key)
	}
	mustIssue(t, res, "My.Flag", "key-renamed", im.Warning)
	if flagOf(t, res, "a_b").Blocked {
		t.Fatal("a_b is blocked")
	}
	mustIssue(t, res, "a-b", "key-collision", im.Error)
	mustIssue(t, res, "A-B", "key-collision", im.Error)
}

func TestNonBooleanDefaultIsTheOffVariationWithACatchAll(t *testing.T) {
	f := flagOf(t, translated(t), "color")
	test := f.Environments["test"]
	if test.Default != "red" || len(test.Rules) != 1 {
		t.Fatalf("test = %+v", test)
	}
	if r := test.Rules[0]; r.Description != "Default rule" || len(r.Groups) != 0 || r.Variation != "blue-sky" || r.Rollout != nil {
		t.Fatalf("catch-all = %+v", r)
	}
	if prod := f.Environments["production"]; prod.Default != "red" || len(prod.Rules) != 0 || prod.Enabled {
		t.Fatalf("production = %+v", prod)
	}
}

func TestBooleanDefaultIsTheDefaultRule(t *testing.T) {
	f := flagOf(t, translated(t), "bool-flag")
	if e := f.Environments["test"]; e.Default != "true" || len(e.Rules) != 0 || !e.Enabled {
		t.Fatalf("test = %+v", e)
	}
}

func TestBooleanOffVariationTrue(t *testing.T) {
	res := translated(t)
	is := mustIssue(t, res, "kill-switch", "off-variation", im.Error)
	if !reflect.DeepEqual(is.Environments, []string{"test"}) {
		t.Fatalf("off-variation error environments = %v", is.Environments)
	}
	if !flagOf(t, res, "kill-switch").Blocked {
		t.Fatal("kill-switch isn't blocked")
	}
	mustIssue(t, res, "soft-kill", "off-variation", im.Warning)
	if flagOf(t, res, "soft-kill").Blocked {
		t.Fatal("soft-kill is blocked")
	}
}

func TestIndividualTargetsLeadTheRules(t *testing.T) {
	rules := flagOf(t, translated(t), "targeted").Environments["test"].Rules
	want := []im.Rule{
		{Description: "Individual targets", Groups: []im.ConditionGroup{and(in(im.UserID, "u1", "u2"))}, Variation: "true"},
		{Description: "Individual targets", Groups: []im.ConditionGroup{and(in("organization.key", "o1"))}, Variation: "false"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestClauseOperators(t *testing.T) {
	res := translated(t)
	rules := flagOf(t, res, "ops").Environments["test"].Rules
	want := []im.Condition{
		in("country", "US"),
		{Attribute: "email", Operator: im.OpStartsWith, Values: []string{"admin"}},
		{Attribute: "email", Operator: im.OpEndsWith, Values: []string{"@acme.com"}},
		{Attribute: "name", Operator: im.OpContains, Values: []string{"bob"}},
		{Attribute: "email", Operator: im.OpMatchesRegex, Values: []string{"^a.*"}},
		{Attribute: "age", Operator: im.OpLessThan, Values: []string{"1.5"}},
		{Attribute: "age", Operator: im.OpLessThanOrEqual, Values: []string{"2"}},
		{Attribute: "age", Operator: im.OpGreaterThan, Values: []string{"3"}},
		{Attribute: "age", Operator: im.OpGreaterThanOrEqual, Values: []string{"4"}},
		{Attribute: "signup", Operator: im.OpBefore, Values: []string{"2023-11-14T22:13:20Z"}},
		{Attribute: "signup", Operator: im.OpAfter, Values: []string{"2024-01-01T00:00:00Z"}},
		{Attribute: "version", Operator: im.OpSemverEquals, Values: []string{"1.2.3"}},
		{Attribute: "version", Operator: im.OpSemverLess, Values: []string{"2.0"}},
		{Attribute: "version", Operator: im.OpSemverGreater, Values: []string{"1.0.0"}},
		in("beta", "true"),
	}
	if len(rules) != len(want) {
		t.Fatalf("%d rules, want %d: %+v", len(rules), len(want), rules)
	}
	for i, w := range want {
		if got := rules[i].Groups; !reflect.DeepEqual(got, []im.ConditionGroup{and(w)}) {
			t.Errorf("rule %d = %+v, want %+v", i, got, w)
		}
	}
	mustIssue(t, res, "ops", "date-attribute", im.Warning)
	mustIssue(t, res, "ops", "version-format", im.Warning)
	mustIssue(t, res, "ops", "regex-dialect", im.Info)
	mustIssue(t, res, "bad-op", "unsupported-operator", im.Error)
}

func TestNegatedClauseGetsAPresenceGuard(t *testing.T) {
	rules := flagOf(t, translated(t), "negated").Environments["test"].Rules
	neg := in("country", "US")
	neg.Negate = true
	if got := rules[0].Groups; !reflect.DeepEqual(got, []im.ConditionGroup{and(neg, present("country"))}) {
		t.Fatalf("negated country = %+v", got)
	}
	key := in(im.UserID, "u1")
	key.Negate = true
	if got := rules[1].Groups; !reflect.DeepEqual(got, []im.ConditionGroup{and(key)}) {
		t.Fatalf("negated key = %+v", got)
	}
}

func TestContextKindsFlatten(t *testing.T) {
	res := translated(t)
	rules := flagOf(t, res, "kinds").Environments["test"].Rules
	if got := rules[0].Groups; !reflect.DeepEqual(got, []im.ConditionGroup{and(in("organization.plan", "gold"))}) {
		t.Fatalf("organization clause = %+v", got)
	}
	if got := rules[1].Groups; !reflect.DeepEqual(got, []im.ConditionGroup{and(in("email", "a@b.c"))}) {
		t.Fatalf("/email clause = %+v", got)
	}
	mustIssue(t, res, "kinds", "context-kind", im.Warning)
	mustIssue(t, res, "nested-attr", "nested-attribute", im.Error)
	mustIssue(t, res, "kind-clause", "context-kind-clause", im.Error)
}

func TestRollouts(t *testing.T) {
	res := translated(t)
	rules := flagOf(t, res, "rollouts").Environments["test"].Rules
	if r := rules[0]; r.Variation != "true" || !reflect.DeepEqual(r.Rollout, intp(30)) || r.BucketBy != im.UserID {
		t.Fatalf("30/70 = %+v", r)
	}
	if r := rules[1]; r.Variation != "true" || r.Rollout != nil {
		t.Fatalf("100/0 = %+v", r)
	}
	if r := rules[2]; !reflect.DeepEqual(r.Rollout, intp(50)) {
		t.Fatalf("experiment = %+v", r)
	}
	mustIssue(t, res, "rollouts", "experiment", im.Warning)
	if r := rules[3]; r.BucketBy != "email" || !reflect.DeepEqual(r.Rollout, intp(10)) {
		t.Fatalf("bucketBy email = %+v", r)
	}
	if r := rules[4]; r.BucketBy != "organization.key" || !reflect.DeepEqual(r.Rollout, intp(20)) {
		t.Fatalf("organization rollout = %+v", r)
	}

	three := flagOf(t, res, "three-way")
	test := three.Environments["test"]
	last := test.Rules[len(test.Rules)-1]
	if test.Default != "a" || last.Variation != "a" || !reflect.DeepEqual(last.Rollout, intp(33)) {
		t.Fatalf("three-way test = %+v", test)
	}
	mustIssue(t, res, "three-way", "rollout-rounded", im.Warning)
	is := mustIssue(t, res, "three-way", "uneven-split", im.Error)
	if !reflect.DeepEqual(is.Environments, []string{"production"}) {
		t.Fatalf("uneven-split environments = %v", is.Environments)
	}
}

func TestSplit(t *testing.T) {
	w := func(ws ...int) []weighted {
		out := make([]weighted, len(ws))
		for i, x := range ws {
			out[i] = weighted{Variation: i, Weight: x}
		}
		return out
	}
	cases := []struct {
		name        string
		ws          []weighted
		n, boolTrue int
		target, pct int
		exact, ok   bool
	}{
		{"two-way", w(30000, 70000), 2, 0, 0, 30, true, true},
		{"boolean prefers true", w(70000, 30000), 2, 1, 1, 30, true, true},
		{"single", w(0, 100000), 2, 0, 1, 100, true, true},
		{"fractional", w(33500, 66500), 2, -1, 0, 34, false, true},
		{"tiny", w(400, 99600), 2, 0, 0, 1, false, true},
		{"even three", w(40000, 30000, 30000), 3, -1, 0, 40, true, true},
		{"uneven three", w(50000, 50000, 0), 3, -1, 0, 0, false, false},
	}
	for _, c := range cases {
		target, pct, exact, ok := split(c.ws, c.n, c.boolTrue)
		if ok != c.ok || (ok && (target != c.target || pct != c.pct || exact != c.exact)) {
			t.Errorf("%s: got %d %d%% exact=%v ok=%v, want %d %d%% exact=%v ok=%v",
				c.name, target, pct, exact, ok, c.target, c.pct, c.exact, c.ok)
		}
	}
}

func TestDisabledRuleIsSkipped(t *testing.T) {
	res := translated(t)
	if rules := flagOf(t, res, "disabled-rule").Environments["test"].Rules; len(rules) != 0 {
		t.Fatalf("rules = %+v", rules)
	}
	mustIssue(t, res, "disabled-rule", "rule-disabled", im.Warning)
}

func TestFallthroughRolloutBecomesTheCatchAll(t *testing.T) {
	e := flagOf(t, translated(t), "fallthrough-rollout").Environments["test"]
	if e.Default != "false" || len(e.Rules) != 1 {
		t.Fatalf("env = %+v", e)
	}
	if r := e.Rules[0]; r.Description != "Default rule" || r.Variation != "true" || !reflect.DeepEqual(r.Rollout, intp(25)) || len(r.Groups) != 0 {
		t.Fatalf("catch-all = %+v", r)
	}
}

func TestClientSideDeprecatedAndArchived(t *testing.T) {
	res := translated(t)
	if !flagOf(t, res, "bool-flag").ClientSide || flagOf(t, res, "color").ClientSide {
		t.Fatal("client-side availability not carried")
	}
	mustIssue(t, res, "deprecated", "deprecated", im.Info)
	found := false
	for _, is := range res.Report.Issues() {
		if is.Code == "archived" && strings.Contains(is.Message, `"gone"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("no archived note for gone")
	}
}

func TestProjectNotes(t *testing.T) {
	res := translated(t)
	mustIssue(t, res, "", "rebucketing", im.Info)
	mustIssue(t, res, "", "not-read", im.Info)
	mustIssue(t, res, "", "case-insensitive", im.Warning)
	if res.Source != "LaunchDarkly" {
		t.Fatalf("source = %q", res.Source)
	}
}

func segmentOf(res *im.Result, source string) *im.Segment {
	for _, s := range res.Project.Segments {
		if s.SourceKey == source {
			return &s
		}
	}
	return nil
}

func negated(c im.Condition) im.Condition { return c.Negated() }

func TestIdenticalSegmentIsExported(t *testing.T) {
	res := translated(t)
	sg := segmentOf(res, "beta-users")
	if sg == nil {
		t.Fatal("beta-users isn't exported")
	}
	want := []im.Condition{{Attribute: "email", Operator: im.OpEndsWith, Values: []string{"@acme.com"}}}
	if sg.Key != "beta-users" || !reflect.DeepEqual(sg.Conditions, want) {
		t.Fatalf("beta-users = %+v", sg)
	}
	r := flagOf(t, res, "uses-beta").Environments["test"].Rules[0]
	if r.SegmentKey != "beta-users" || len(r.Groups) != 0 {
		t.Fatalf("uses-beta rule = %+v", r)
	}
}

func TestSegmentThatDiffersByEnvironmentIsInlined(t *testing.T) {
	res := translated(t)
	if segmentOf(res, "staff") != nil {
		t.Fatal("staff was exported")
	}
	mustIssue(t, res, "staff", "segment-inlined", im.Info)
	f := flagOf(t, res, "uses-staff")
	for env, team := range map[string]string{"test": "eng", "production": "ops"} {
		if got := f.Environments[env].Rules[0].Groups; !reflect.DeepEqual(got, []im.ConditionGroup{and(in("team", team))}) {
			t.Fatalf("%s: %+v", env, got)
		}
	}
}

func TestIncludedExcludedAndRules(t *testing.T) {
	rules := flagOf(t, translated(t), "uses-vip").Environments["test"].Rules
	want := [][]im.ConditionGroup{
		{and(in(im.UserID, "u1"))},
		{and(in("plan", "gold"), negated(in(im.UserID, "u2")))},
	}
	if len(rules) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
	for i, w := range want {
		if !reflect.DeepEqual(rules[i].Groups, w) {
			t.Fatalf("rule %d = %+v, want %+v", i, rules[i].Groups, w)
		}
	}
}

func TestNegatedSegmentStaysOneRule(t *testing.T) {
	rules := flagOf(t, translated(t), "not-vip").Environments["test"].Rules
	or := func(cs ...im.Condition) im.ConditionGroup { return im.ConditionGroup{Operator: im.Or, Conditions: cs} }
	want := []im.ConditionGroup{
		or(negated(in(im.UserID, "u1"))),
		or(negated(in("plan", "gold")), in(im.UserID, "u2")),
	}
	if len(rules) != 1 || !reflect.DeepEqual(rules[0].Groups, want) {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestUnusableSegments(t *testing.T) {
	res := translated(t)
	mustIssue(t, res, "big", "unbounded", im.Error)
	mustIssue(t, res, "weighted", "weighted-segment-rule", im.Error)
	mustIssue(t, res, "nested", "nested-segment", im.Error)
	for _, sg := range []string{"big", "weighted", "nested"} {
		if is := issue(res, sg, "segment-inlined"); is != nil {
			t.Fatalf("%s: an untranslatable segment says it is inlined: %+v", sg, is)
		}
	}
	for _, f := range []string{"uses-big", "uses-weighted", "uses-nested"} {
		mustIssue(t, res, f, "segment-unusable", im.Error)
		if !flagOf(t, res, f).Blocked {
			t.Fatalf("%s isn't blocked", f)
		}
	}
}

func TestEmptySegmentIsNeverExported(t *testing.T) {
	res := translated(t)
	if segmentOf(res, "empty") != nil {
		t.Fatal("an empty segment would match everyone in Featureflip")
	}
	if rules := flagOf(t, res, "uses-empty").Environments["test"].Rules; len(rules) != 0 {
		t.Fatalf("rules = %+v", rules)
	}
	mustIssue(t, res, "uses-empty", "never-matches", im.Info)
}

func TestMissingSegmentMatchesNoOne(t *testing.T) {
	f := flagOf(t, translated(t), "uses-missing")
	if rules := f.Environments["test"].Rules; len(rules) != 0 {
		t.Fatalf("test rules = %+v", rules)
	}
	if rules := f.Environments["production"].Rules; len(rules) != 1 || rules[0].SegmentKey != "missing-in-test" {
		t.Fatalf("production rules = %+v", rules)
	}
}

func TestUnusedNonExportableSegmentIsReported(t *testing.T) {
	mustIssue(t, translated(t), "unused-diff", "segment-not-imported", im.Warning)
}

func TestPrerequisitesTranslate(t *testing.T) {
	res := translated(t)
	for _, env := range []string{"test", "production"} {
		if got := flagOf(t, res, "child").Environments[env].Prerequisites; !reflect.DeepEqual(got, []im.Prerequisite{{Flag: "parent", Variation: "true"}}) {
			t.Fatalf("%s: %+v", env, got)
		}
	}
	if issue(res, "child", "prerequisite-off") != nil {
		t.Fatal("child warns about an off prerequisite")
	}
}

func TestPrerequisiteOnAnOffVariationWarns(t *testing.T) {
	is := mustIssue(t, translated(t), "child-of-false", "prerequisite-off", im.Warning)
	if len(is.Environments) != 2 {
		t.Fatalf("environments = %v", is.Environments)
	}
}

func TestBlockedPrerequisiteBlocksTheChain(t *testing.T) {
	res := translated(t)
	for _, f := range []string{"needs-kill", "needs-needs-kill", "needs-gone"} {
		mustIssue(t, res, f, "prerequisite-blocked", im.Error)
		if !flagOf(t, res, f).Blocked {
			t.Fatalf("%s isn't blocked", f)
		}
	}
	if flagOf(t, res, "child").Blocked {
		t.Fatal("child is blocked")
	}
}

func TestPrerequisiteCycleIsBlocked(t *testing.T) {
	snap := loadSnapshot(t)
	cyc := func(key, needs string) flag {
		f := flag{Key: key, Kind: "boolean", Variations: []variation{{Value: json.RawMessage("true")}, {Value: json.RawMessage("false")}},
			Environments: map[string]*flagEnv{}}
		for _, e := range snap.Environments {
			one := 1
			f.Environments[e.Key] = &flagEnv{On: true, OffVariation: &one, Fallthrough: serve{Variation: &one}, Prerequisites: []prereq{{Key: needs}}}
		}
		return f
	}
	snap.Flags = append(snap.Flags, cyc("loop-a", "loop-b"), cyc("loop-b", "loop-a"))
	res, err := translate(snap, im.Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustIssue(t, res, "loop-a", "prerequisite-cycle", im.Error)
	mustIssue(t, res, "loop-b", "prerequisite-cycle", im.Error)
}

func TestEveryReadyFlagIsWellFormed(t *testing.T) {
	res := translated(t)
	exported := map[string]bool{}
	for _, s := range res.Project.Segments {
		exported[s.Key] = true
		if len(s.Conditions) == 0 {
			t.Errorf("segment %s has no conditions", s.Key)
		}
	}
	flags := map[string]im.Flag{}
	for _, f := range res.Project.Flags {
		flags[f.Key] = f
	}
	hasVar := func(f im.Flag, key string) bool {
		for _, v := range f.Variations {
			if v.Key == key {
				return true
			}
		}
		return false
	}
	for _, f := range res.Project.Flags {
		if f.Blocked {
			continue
		}
		for env, ec := range f.Environments {
			if !hasVar(f, ec.Default) {
				t.Errorf("%s/%s: default %q isn't a variation", f.Key, env, ec.Default)
			}
			for _, r := range ec.Rules {
				if !hasVar(f, r.Variation) {
					t.Errorf("%s/%s: rule serves %q", f.Key, env, r.Variation)
				}
				if r.SegmentKey != "" && !exported[r.SegmentKey] {
					t.Errorf("%s/%s: rule uses unexported segment %q", f.Key, env, r.SegmentKey)
				}
				if r.Rollout != nil && (*r.Rollout < 1 || *r.Rollout > 99) {
					t.Errorf("%s/%s: rollout %d", f.Key, env, *r.Rollout)
				}
				for _, g := range r.Groups {
					for _, c := range g.Conditions {
						if c.Attribute == "" || len(c.Values) == 0 {
							t.Errorf("%s/%s: malformed condition %+v", f.Key, env, c)
						}
					}
				}
			}
			for _, pr := range ec.Prerequisites {
				p, ok := flags[pr.Flag]
				if !ok || p.Blocked || !hasVar(p, pr.Variation) {
					t.Errorf("%s/%s: prerequisite %+v isn't a ready flag's variation", f.Key, env, pr)
				}
			}
		}
	}
}

func TestEveryNonUserKindWarnsOncePerItem(t *testing.T) {
	res := translated(t)
	for _, c := range []struct{ subject, kind string }{
		{"targeted", "flag"}, {"rollouts", "flag"}, {"kinds", "flag"}, {"org-members", "segment"},
	} {
		var found []im.Issue
		for _, is := range res.Report.Issues() {
			if is.Subject == c.subject && is.Code == "context-kind" {
				found = append(found, is)
			}
		}
		if len(found) != 1 || string(found[0].Kind) != c.kind || found[0].Severity != im.Warning ||
			!strings.Contains(found[0].Message, "organization.key") {
			t.Errorf("%s: context-kind issues = %+v", c.subject, found)
		}
	}
}
