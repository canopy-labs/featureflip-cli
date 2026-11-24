package launchdarkly

import (
	"bytes"
	"context"
	"encoding/json"
	goflag "flag"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

var liveConfig = goflag.String("live", "", "path to a JSON file naming a disposable Featureflip project to import into")

// liveTarget is the -live file: a disposable project whose environments
// include development and production, a token for it, and a server SDK key
// for its production environment.
type liveTarget struct {
	URL     string            `json:"url"`
	EvalURL string            `json:"evalUrl"`
	Token   string            `json:"token"`
	Org     string            `json:"org"`
	Project string            `json:"project"`
	SDK     map[string]string `json:"sdk"`
}

// TestApplyToALiveProject writes the fixture project into a real Featureflip
// project, checks that a second plan finds every written item unchanged (true
// only if the API hands back exactly what was written), and then evaluates
// contexts through the real Evaluation API, comparing each with what
// LaunchDarkly serves them. It runs only when pointed at a disposable project:
//
//	go test ./internal/importer/launchdarkly -run Live -v -args -live=/path/to/target.json
func TestApplyToALiveProject(t *testing.T) {
	if *liveConfig == "" {
		t.Skip("pass -args -live=<file> to run against a disposable project")
	}
	raw, err := os.ReadFile(*liveConfig)
	if err != nil {
		t.Fatal(err)
	}
	var target liveTarget
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	if target.EvalURL == "" {
		target.EvalURL = "http://localhost:5002"
	}

	ctx := context.Background()
	opts := im.ApplyOptions{Org: target.Org, Project: target.Project, CarryState: true}
	client := api.New(api.Options{BaseURL: target.URL, Token: target.Token,
		RateLimit: &api.RateLimitPolicy{MaxWait: 2 * time.Minute, Retries: 10, OnWait: func(d time.Duration) { t.Logf("rate limited; waiting %s", d) }}})
	load := func() *im.Result {
		res, err := translate(loadSnapshot(t), im.Options{EnvMap: map[string]string{"test": "development"}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	first, err := im.BuildPlan(ctx, client, load(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Apply(ctx, client, func(st *im.Step) { t.Logf("%s %s %s", st.Action, st.Kind, st.Key) }); err != nil {
		t.Fatal(err)
	}

	second, err := im.BuildPlan(ctx, client, load(), opts)
	if err != nil {
		t.Fatal(err)
	}
	// Compare by item, not position: a step that no longer writes no longer
	// needs its prerequisites ordered before it.
	after := map[string]*im.Step{}
	for _, st := range second.Steps {
		after[string(st.Kind)+" "+st.Key] = st
	}
	wrote := 0
	for _, before := range first.Steps {
		st := after[string(before.Kind)+" "+before.Key]
		if before.Done {
			wrote++
		}
		if (before.Done || before.Action == im.ActUnchanged) && st.Action != im.ActUnchanged {
			t.Errorf("%s %s was %s but now plans as %s %v: %s", st.Kind, st.Key, before.Action, st.Action, st.Environments, st.Why)
		}
	}
	t.Logf("%d items written; every one read back unchanged", wrote)

	for _, c := range liveCases() {
		got := evaluate(t, target, c.flag, c.context)
		if got.status != c.status || (c.status == 200 && (got.Variation != c.variation || (c.reason != "" && got.Reason != c.reason))) {
			t.Errorf("%s with %v: got %d %q (%s), LaunchDarkly serves %q%s",
				c.flag, c.context, got.status, got.Variation, got.Reason, c.variation, map[bool]string{true: " (" + c.reason + ")", false: ""}[c.reason != ""])
		}
	}

	// Rollouts: the share, not the members, must carry over.
	for _, r := range []struct {
		flag    string
		attrs   map[string]any
		percent int
	}{
		{"fallthrough-rollout", nil, 25},
		{"rollouts", map[string]any{"country": "US"}, 30},
	} {
		trues := 0
		const n = 400
		for i := range n {
			ctxt := map[string]any{"user_id": fmt.Sprintf("user-%d", i)}
			for k, v := range r.attrs {
				ctxt[k] = v
			}
			if evaluate(t, target, r.flag, ctxt).Variation == "true" {
				trues++
			}
		}
		if share := trues * 100 / n; share < r.percent-8 || share > r.percent+8 {
			t.Errorf("%s: %d%% served true, LaunchDarkly serves %d%%", r.flag, share, r.percent)
		} else {
			t.Logf("%s: %d%% served true (LaunchDarkly %d%%)", r.flag, share, r.percent)
		}
	}
}

type liveCase struct {
	flag      string
	context   map[string]any
	status    int
	variation string
	reason    string
}

// liveCases are contexts whose LaunchDarkly result, worked out by hand from
// the fixture's production environment, the translation must reproduce.
func liveCases() []liveCase {
	u := func(id string, kv ...any) map[string]any {
		m := map[string]any{"user_id": id}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	ok := func(flag string, ctx map[string]any, variation string) liveCase {
		return liveCase{flag: flag, context: ctx, status: 200, variation: variation}
	}
	return []liveCase{
		ok("bool-flag", u("a"), "true"),
		// Off in production: LaunchDarkly serves the off variation.
		ok("color", u("a"), "red"),
		ok("targeted", u("u1"), "true"),
		ok("targeted", u("z", "organization.key", "o1"), "false"),
		ok("targeted", u("z"), "false"),
		// A negated clause doesn't match a context without the attribute.
		ok("negated", u("u1"), "false"),
		ok("negated", u("u1", "country", "FR"), "true"),
		ok("negated", u("u1", "country", "US"), "false"),
		ok("ops", u("a", "email", "bob@acme.com"), "true"),
		ok("ops", u("a", "version", "1.2.3"), "true"),
		ok("ops", u("a", "age", 1), "true"),
		ok("ops", u("a", "signup", "2023-01-01T00:00:00Z"), "true"),
		ok("ops", u("a", "country", "FR"), "false"),
		ok("kinds", u("a", "organization.plan", "gold"), "true"),
		ok("kinds", u("a", "plan", "gold"), "false"),
		ok("uses-beta", u("a", "email", "x@acme.com"), "true"),
		ok("uses-beta", u("a", "email", "x@other.com"), "false"),
		ok("uses-staff", u("a", "team", "ops"), "true"),
		ok("uses-staff", u("a", "team", "eng"), "false"),
		ok("uses-vip", u("u1"), "true"),
		ok("uses-vip", u("u2", "plan", "gold"), "false"),
		ok("uses-vip", u("u3", "plan", "gold"), "true"),
		ok("not-vip", u("u1"), "false"),
		ok("not-vip", u("u2", "plan", "gold"), "true"),
		ok("not-vip", u("u3", "plan", "silver"), "true"),
		ok("not-vip", u("u3", "plan", "gold"), "false"),
		ok("uses-missing", u("p1"), "true"),
		// child needs parent=true; parent's default rule serves false.
		{flag: "child", context: u("a"), status: 200, variation: "false", reason: "prerequisite-failed"},
		// Blocked, so never written.
		{flag: "kill-switch", context: u("a"), status: 404},
	}
}

type evalResult struct {
	status    int
	Variation string `json:"variation"`
	Reason    string `json:"reason"`
}

// evaluate asks the Evaluation API, retrying briefly while a fresh write
// reaches its cache.
func evaluate(t *testing.T, target liveTarget, flagKey string, ctxt map[string]any) evalResult {
	t.Helper()
	userID, _ := ctxt["user_id"].(string)
	attrs := map[string]any{}
	for k, v := range ctxt {
		if k != "user_id" {
			attrs[k] = v
		}
	}
	body, _ := json.Marshal(map[string]any{"flagKey": flagKey, "context": map[string]any{"userId": userID, "attributes": attrs}})
	var out evalResult
	for attempt := 0; attempt < 10; attempt++ {
		req, _ := http.NewRequest("POST", target.EvalURL+"/v1/sdk/evaluate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", target.SDK["production"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out = evalResult{status: resp.StatusCode}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if out.status != 404 && out.status < 500 {
			return out
		}
		time.Sleep(500 * time.Millisecond)
	}
	return out
}
