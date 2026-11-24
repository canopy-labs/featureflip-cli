package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeStatsig is a minimal Statsig Console API: two environments, a gate that
// translates, a gate that doesn't, and no segments or configs.
func fakeStatsig(t *testing.T) *httptest.Server {
	t.Helper()
	data := map[string]string{
		"/console/v1/environments": `{"environments":[{"name":"development","id":"1"},{"name":"production","id":"2"}]}`,
		"/console/v1/gates": `[
			{"id":"new_checkout","name":"New checkout","idType":"userID","status":"In Progress","isEnabled":true,
			 "rules":[{"id":"r1","name":"Half","passPercentage":50,"conditions":[{"type":"email","operator":"any","targetValue":["a@x.com"]}]}]},
			{"id":"needs_other","idType":"userID","status":"In Progress","isEnabled":true,
			 "rules":[{"id":"r2","name":"Chained","passPercentage":100,"conditions":[{"type":"passes_gate","operator":"any","targetValue":["new_checkout"]}]}]}]`,
		"/console/v1/dynamic_configs": `[]`,
		"/console/v1/segments":        `[]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("STATSIG-API-KEY") != "console-test" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"status":401,"message":"Invalid key"}`)
			return
		}
		body, ok := data[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"status":404,"message":"not found"}`)
			return
		}
		fmt.Fprintf(w, `{"message":"ok","data":%s,"pagination":{"pageNumber":1,"nextPage":null}}`, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func statsigEnv(srv *httptest.Server) map[string]string {
	return map[string]string{"STATSIG_CONSOLE_API_KEY": "console-test", "STATSIG_CONSOLE_API_URL": srv.URL}
}

func TestImportStatsigDryRunNeedsNoFeatureflipAccount(t *testing.T) {
	srv := fakeStatsig(t)
	stdout, stderr, code := run(t, runOpts{Env: statsigEnv(srv), NoToken: true}, "import", "statsig")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"Statsig import: dry run, nothing was written.",
		"production   -> production",
		"Flags     2 read: 1 ready, 1 blocked",
		"flag needs_other: rule \"Chained\": it checks another gate (passes_gate), which has no Featureflip equivalent",
		"user_id, email",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestImportStatsigJSON(t *testing.T) {
	srv := fakeStatsig(t)
	stdout, _, code := run(t, runOpts{Env: statsigEnv(srv), NoToken: true}, "import", "statsig", "--json", "--env-map", "production=prod")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	var doc struct {
		Source  string `json:"source"`
		DryRun  bool   `json:"dryRun"`
		Summary struct {
			Flags struct{ Read, Ready, Blocked int } `json:"flags"`
		} `json:"summary"`
		Project struct {
			Environments []struct{ Key, Source string } `json:"environments"`
			Flags        []struct {
				Key          string                     `json:"key"`
				Blocked      bool                       `json:"blocked"`
				Environments map[string]json.RawMessage `json:"environments"`
			} `json:"flags"`
		} `json:"project"`
		Issues []struct{ Severity, Subject, Code string } `json:"issues"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if doc.Source != "Statsig" || !doc.DryRun || doc.Summary.Flags.Read != 2 || doc.Summary.Flags.Ready != 1 || doc.Summary.Flags.Blocked != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Project.Environments[1].Key != "prod" {
		t.Fatalf("environments = %+v", doc.Project.Environments)
	}
	if _, ok := doc.Project.Flags[0].Environments["prod"]; !ok {
		t.Fatalf("flag environments = %v", doc.Project.Flags[0].Environments)
	}
	if doc.Issues[0].Severity != "error" || doc.Issues[0].Subject != "needs_other" {
		t.Fatalf("issues = %+v", doc.Issues)
	}
}

func TestImportStatsigErrors(t *testing.T) {
	srv := fakeStatsig(t)
	cases := []struct {
		name string
		env  map[string]string
		args []string
		code int
		msg  string
	}{
		{"no key", map[string]string{"STATSIG_CONSOLE_API_URL": srv.URL}, nil, 3, "STATSIG_CONSOLE_API_KEY"},
		{"refused key", map[string]string{"STATSIG_CONSOLE_API_KEY": "wrong", "STATSIG_CONSOLE_API_URL": srv.URL}, nil, 3, "refused the Console API key"},
		{"malformed map", statsigEnv(srv), []string{"--env-map", "production"}, 2, "use source=featureflip"},
		{"invalid key", statsigEnv(srv), []string{"--env-map", "production=Prod Env"}, 2, "not a valid environment key"},
		{"unknown env", statsigEnv(srv), []string{"--env-map", "qa=qa"}, 2, "it has development, production"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stderr, code := run(t, runOpts{Env: c.env, NoToken: true}, append([]string{"import", "statsig"}, c.args...)...)
			if code != c.code || !strings.Contains(stderr, c.msg) {
				t.Fatalf("exit %d, stderr %q; want exit %d mentioning %q", code, stderr, c.code, c.msg)
			}
			if strings.Contains(stderr, "wrong") {
				t.Fatalf("the key leaked into the error: %q", stderr)
			}
		})
	}
}

// fakeProject serves an empty Featureflip project acme/web with dev and prod.
func fakeProject(t *testing.T, flagCap int) *fakeAPI {
	t.Helper()
	api := newFakeAPI(t)
	base := "/api/v1/orgs/acme/projects/web"
	api.on("GET", "/api/v1/orgs/acme", 200, fmt.Sprintf(`{"id":"o","name":"Acme","slug":"acme","plan":"Solo","limits":{"flagsPerProject":%d}}`, flagCap))
	api.on("GET", base+"/environments", 200, `{"items":[{"id":"e1","key":"dev","name":"Dev","sortOrder":0},{"id":"e2","key":"prod","name":"Prod","sortOrder":1}],"next_cursor":null}`)
	api.on("GET", base+"/flags", 200, `{"items":[],"next_cursor":null}`)
	api.on("GET", base+"/segments", 200, `{"items":[],"next_cursor":null}`)
	api.on("POST", base+"/flags", 201, `{"id":"f1","key":"new_checkout","name":"New checkout","type":"Boolean","variations":[{"id":"vt","key":"true","name":"True","value":"true"},{"id":"vf","key":"false","name":"False","value":"false"}]}`)
	api.on("PUT", base+"/flags/new_checkout/environments/dev/targeting", 204, "")
	api.on("PUT", base+"/flags/new_checkout/environments/prod/targeting", 204, "")
	return api
}

func applyArgs(extra ...string) []string {
	return append([]string{"import", "statsig", "--org", "acme", "--project", "web", "--env-map", "development=dev,production=prod", "--apply"}, extra...)
}

func TestImportStatsigApply(t *testing.T) {
	srv := fakeStatsig(t)
	api := fakeProject(t, 10)
	stdout, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api}, applyArgs("--client-side")...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"Statsig import into acme/web.", "Flags     1 created, 1 blocked", "--carry-state"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "Created flag new_checkout") {
		t.Errorf("stderr = %q", stderr)
	}

	creates := api.calls("POST", "/api/v1/orgs/acme/projects/web/flags")
	if len(creates) != 1 || !strings.Contains(creates[0].Body, `"clientSideVisible":true`) || creates[0].Header.Get("Idempotency-Key") == "" {
		t.Fatalf("creates = %+v", creates)
	}
	put := api.calls("PUT", "/api/v1/orgs/acme/projects/web/flags/new_checkout/environments/prod/targeting")
	if len(put) != 1 || !strings.Contains(put[0].Body, `"variationId":"vt"`) || !strings.Contains(put[0].Body, `"rolloutPercentage":50`) {
		t.Fatalf("targeting = %+v", put)
	}
	if got := put[0].Header.Get("X-Intent-Reason"); !strings.Contains(got, "Statsig") {
		t.Errorf("change reason = %q", got)
	}
	if api.count("POST")+api.count("PUT") != 3 {
		t.Errorf("unexpected writes: %d POST, %d PUT", api.count("POST"), api.count("PUT"))
	}
}

func TestImportStatsigApplyJSON(t *testing.T) {
	srv := fakeStatsig(t)
	stdout, _, code := run(t, runOpts{Env: statsigEnv(srv), API: fakeProject(t, 10)}, applyArgs("--json")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	var doc struct {
		DryRun bool `json:"dryRun"`
		Target struct {
			Steps []struct {
				Key, Action string
				Done        bool
			} `json:"steps"`
		} `json:"target"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if doc.DryRun || len(doc.Target.Steps) != 2 || doc.Target.Steps[0].Action != "create" || !doc.Target.Steps[0].Done || doc.Target.Steps[1].Action != "blocked" {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestImportStatsigApplyOverThePlanCap(t *testing.T) {
	srv := fakeStatsig(t)
	api := fakeProject(t, 0)
	stdout, _, code := run(t, runOpts{Env: statsigEnv(srv), API: api}, applyArgs("--json")...)
	if code != 5 || !strings.Contains(stdout, `"error": "plan_limit_reached"`) || !strings.Contains(stdout, "Nothing was written") {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	if api.count("POST")+api.count("PUT") != 0 {
		t.Fatal("wrote past the plan cap check")
	}
}

func TestImportStatsigApplyRefusesThePlanCapBeforeAskingToConfirm(t *testing.T) {
	srv := fakeStatsig(t)
	api := fakeProject(t, 0)
	api.on("GET", "/api/v1/orgs/acme/projects/web/environments", 200,
		`{"items":[{"id":"e1","key":"dev","name":"Dev","sortOrder":0},{"id":"e2","key":"prod","name":"Prod","sortOrder":1,"requireConfirmation":true}],"next_cursor":null}`)
	term := &fakeTerminal{interactive: true, answers: []string{"prod"}}
	_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api, Terminal: term}, applyArgs("--carry-state")...)
	if code != 5 || !strings.Contains(stderr, "Nothing was written") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(term.prompts) != 0 {
		t.Fatalf("asked to confirm an import the plan cap refuses: %q", term.prompts)
	}
}

func TestImportStatsigApplyJSONFailureSaysWhatWasWritten(t *testing.T) {
	srv := fakeStatsig(t)
	api := fakeProject(t, 10)
	api.on("PUT", "/api/v1/orgs/acme/projects/web/flags/new_checkout/environments/prod/targeting", 422,
		`{"error":"validation_failed","message":"Rule is invalid.","docs_url":"d"}`)
	stdout, _, code := run(t, runOpts{Env: statsigEnv(srv), API: api}, applyArgs("--json")...)
	if code != 5 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	var doc struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Target  struct {
			Steps []struct {
				Key, Action   string
				Done, Created bool
			} `json:"steps"`
		} `json:"target"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if doc.Error != "validation_failed" || !strings.Contains(doc.Message, "was created but not fully configured") {
		t.Fatalf("doc = %+v", doc)
	}
	if len(doc.Target.Steps) == 0 || doc.Target.Steps[0].Key != "new_checkout" || !doc.Target.Steps[0].Created || doc.Target.Steps[0].Done {
		t.Fatalf("target = %+v, want new_checkout created and not done", doc.Target)
	}
}

func TestImportStatsigApplyErrors(t *testing.T) {
	srv := fakeStatsig(t)
	t.Run("apply options need --apply", func(t *testing.T) {
		_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), NoToken: true}, "import", "statsig", "--carry-state")
		if code != 2 || !strings.Contains(stderr, "--carry-state only applies with --apply") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	})
	t.Run("apply needs a token", func(t *testing.T) {
		_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), NoToken: true}, applyArgs()...)
		if code != 3 || !strings.Contains(stderr, "FEATUREFLIP_TOKEN") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	})
	t.Run("unmapped environment", func(t *testing.T) {
		_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: fakeProject(t, 10)},
			"import", "statsig", "--org", "acme", "--project", "web", "--apply")
		if code != 2 || !strings.Contains(stderr, "no environment development (from development), production (from production)") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	})
}

func TestImportStatsigApplyWaitsOutTheRateLimit(t *testing.T) {
	srv := fakeStatsig(t)
	api := fakeProject(t, 10)
	limited := 0
	api.handle("GET", "/api/v1/orgs/acme/projects/web/segments", func(w http.ResponseWriter, r *http.Request) {
		if limited < 2 {
			limited++
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":"rate_limited","message":"Rate limit exceeded.","docs_url":"d","retry_after":60}`)
			return
		}
		fmt.Fprint(w, `{"items":[],"next_cursor":null}`)
	})
	var sleeps []time.Duration
	_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api, Sleeps: &sleeps}, applyArgs()...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Count(stderr, "Rate limited by the Featureflip API; waiting 1m0s") != 2 || len(sleeps) != 2 {
		t.Fatalf("sleeps %v, stderr %q", sleeps, stderr)
	}
}

func TestImportStatsigApplyAsksBeforeLiveChangesInConfirmedEnvironments(t *testing.T) {
	srv := fakeStatsig(t)
	newAPI := func() *fakeAPI {
		api := fakeProject(t, 10)
		api.on("GET", "/api/v1/orgs/acme/projects/web/environments", 200,
			`{"items":[{"id":"e1","key":"dev","name":"Dev","sortOrder":0},{"id":"e2","key":"prod","name":"Prod","sortOrder":1,"requireConfirmation":true}],"next_cursor":null}`)
		api.on("POST", "/api/v1/orgs/acme/projects/web/flags/new_checkout/environments/dev/toggle", 200, `{}`)
		api.on("POST", "/api/v1/orgs/acme/projects/web/flags/new_checkout/environments/prod/toggle", 200, `{}`)
		return api
	}

	api := newAPI()
	_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api}, applyArgs("--carry-state")...)
	if code != 6 || !strings.Contains(stderr, "prod requires confirmation") || api.count("POST")+api.count("PUT") != 0 {
		t.Fatalf("exit %d, stderr %q, writes %d", code, stderr, api.count("POST")+api.count("PUT"))
	}

	api = newAPI()
	term := &fakeTerminal{interactive: true, answers: []string{"prod"}}
	if _, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api, Terminal: term}, applyArgs("--carry-state")...); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(term.prompts) != 1 || !strings.Contains(term.prompts[0], `Type "prod"`) {
		t.Fatalf("prompts = %q", term.prompts)
	}

	api = newAPI()
	if _, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api}, applyArgs("--carry-state", "--yes")...); code != 0 {
		t.Fatalf("--yes: exit %d: %s", code, stderr)
	}
	if len(api.calls("POST", "/api/v1/orgs/acme/projects/web/flags/new_checkout/environments/prod/toggle")) != 1 {
		t.Fatal("--carry-state should turn new_checkout on in prod")
	}
}

func TestImportStatsigApplyBreakFreeze(t *testing.T) {
	srv := fakeStatsig(t)
	t.Run("needs a reason", func(t *testing.T) {
		_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: fakeProject(t, 10)}, applyArgs("--break-freeze")...)
		if code != 2 || !strings.Contains(stderr, "--break-freeze needs --reason") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	})
	t.Run("needs --apply", func(t *testing.T) {
		_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), NoToken: true}, "import", "statsig", "--break-freeze")
		if code != 2 || !strings.Contains(stderr, "--break-freeze only applies with --apply") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	})
	t.Run("a frozen write names the freeze and the way past it", func(t *testing.T) {
		api := fakeProject(t, 10)
		api.on("POST", "/api/v1/orgs/acme/projects/web/flags", 423, fmt.Sprintf(frozenBody, true))
		_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api}, applyArgs()...)
		if code != 8 || !strings.Contains(stderr, `Change freeze "Holiday freeze" covers production`) ||
			!strings.Contains(stderr, "--break-freeze --reason") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	})
	t.Run("sends the override on every write", func(t *testing.T) {
		api := fakeProject(t, 10)
		_, stderr, code := run(t, runOpts{Env: statsigEnv(srv), API: api}, applyArgs("--break-freeze", "--reason", "launch")...)
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		writes := 0
		for _, r := range append(api.calls("POST", "/api/v1/orgs/acme/projects/web/flags"),
			api.calls("PUT", "/api/v1/orgs/acme/projects/web/flags/new_checkout/environments/prod/targeting")...) {
			writes++
			if r.Header.Get("X-Freeze-Override") != "true" {
				t.Errorf("a write sent no freeze override: %+v", r)
			}
		}
		if writes != 2 {
			t.Errorf("writes = %d", writes)
		}
	})
}
