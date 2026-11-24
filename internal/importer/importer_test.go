package importer

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

var apiKey = regexp.MustCompile(`^[a-z0-9]+([_-][a-z0-9]+)*$`)

func TestNormalizeKey(t *testing.T) {
	cases := map[string]string{
		"new_checkout":       "new_checkout",
		"New Checkout":       "new-checkout",
		"checkout.v2":        "checkout-v2",
		"  Beta  Users! ":    "beta-users",
		"a__b--c":            "a_b-c",
		"_leading-trailing_": "leading-trailing",
		"ÜberFlag":           "berflag",
		"!!!":                "",
		"a b_c":              "a-b_c",
	}
	for in, want := range cases {
		got := NormalizeKey(in)
		if got != want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !apiKey.MatchString(got) {
			t.Errorf("NormalizeKey(%q) = %q, which the API would refuse", in, got)
		}
	}
	long := NormalizeKey(strings.Repeat("ab_", 200))
	if len(long) > maxKeyLength || !apiKey.MatchString(long) {
		t.Errorf("a long key normalized to %d chars, valid=%v", len(long), apiKey.MatchString(long))
	}
}

func TestKeyerReportsEveryOwner(t *testing.T) {
	k := NewKeyer()
	k.Key("Beta Users")
	k.Key("beta-users")
	k.Key("other")
	if got := k.Owners("beta-users"); len(got) != 2 {
		t.Fatalf("owners = %v, want both source keys", got)
	}
	if got := k.Owners("other"); len(got) != 1 {
		t.Fatalf("owners = %v", got)
	}
}

func TestReportMergesEnvironmentsAndOrdersBySeverity(t *testing.T) {
	r := NewReport()
	r.Add(Info, KindProject, "", "note", "a note", "")
	r.Add(Warning, KindFlag, "g", "w", "same warning", "dev")
	r.Add(Warning, KindFlag, "g", "w", "same warning", "prod")
	r.Add(Warning, KindFlag, "g", "w", "same warning", "prod")
	r.Add(Error, KindFlag, "h", "e", "broken", "")

	issues := r.Issues()
	if len(issues) != 3 {
		t.Fatalf("issues = %+v, want 3 (the repeated warning merged)", issues)
	}
	if issues[0].Severity != Error || issues[1].Severity != Warning || issues[2].Severity != Info {
		t.Fatalf("order = %v %v %v, want error, warning, info", issues[0].Severity, issues[1].Severity, issues[2].Severity)
	}
	if got := strings.Join(issues[1].Environments, ","); got != "dev,prod" {
		t.Fatalf("environments = %q, want dev,prod", got)
	}
	if !r.Blocked(KindFlag, "h") || r.Blocked(KindFlag, "g") {
		t.Fatal("Blocked should follow errors only")
	}
}

func project() *Result {
	pct := 30
	p := &Project{
		Environments: []Environment{{Key: "development", Source: "development"}, {Key: "production", Source: "production"}},
		Segments:     []Segment{{Key: "beta", SourceKey: "beta", Conditions: []Condition{{Attribute: "plan", Operator: OpIn, Values: []string{"pro"}}}}},
		Flags: []Flag{
			{Key: "ok", SourceKey: "ok", Environments: map[string]*EnvConfig{
				"production": {Rules: []Rule{{Variation: "true", Rollout: &pct, BucketBy: "stableID",
					Groups: []ConditionGroup{{Operator: And, Conditions: []Condition{{Attribute: "email"}, {Attribute: UserID}}}}}}},
			}},
			{Key: "warned", SourceKey: "warned", Environments: map[string]*EnvConfig{}},
			{Key: "blocked", SourceKey: "blocked", Environments: map[string]*EnvConfig{
				"production": {Rules: []Rule{{Groups: []ConditionGroup{{Conditions: []Condition{{Attribute: "neverListed"}}}}}}},
			}},
		},
	}
	r := NewReport()
	r.Add(Warning, KindFlag, "warned", "w", "check this", "production")
	r.Add(Error, KindFlag, "blocked", "e", "can't translate", "")
	return &Result{Source: "Vendor", Project: p, Report: r}
}

func TestSummarizeAndAttributes(t *testing.T) {
	res := project()
	s := res.Summarize()
	want := Counts{Read: 3, Ready: 2, Warned: 1, Blocked: 1}
	if s.Flags != want {
		t.Fatalf("flags = %+v, want %+v", s.Flags, want)
	}
	if !res.Project.Flags[2].Blocked {
		t.Fatal("Summarize should mark the blocked flag")
	}
	// Blocked flags don't count; the rollout's bucket-by does; user_id leads.
	if got := strings.Join(res.Project.Attributes(), ","); got != "user_id,email,plan,stableID" {
		t.Fatalf("attributes = %q", got)
	}
}

func TestRenderTextShowsEachSection(t *testing.T) {
	var b bytes.Buffer
	if err := project().RenderText(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"Vendor import: dry run, nothing was written.",
		"development  -> development",
		"Flags     3 read: 2 ready (1 with warnings), 1 blocked",
		"Blocked: these are not imported\n  flag blocked: can't translate\n",
		"Warnings: imported, but check these\n  flag warned: check this [production]\n",
		"user_id, email, plan, stableID",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
