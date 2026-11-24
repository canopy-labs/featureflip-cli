package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLaunchDarkly is a minimal LaunchDarkly REST API: two environments, a
// flag, a flag that needs it as a prerequisite, and one that can't translate.
func fakeLaunchDarkly(t *testing.T) *httptest.Server {
	t.Helper()
	envCfg := func(extra string) string {
		return `{"on":true,"offVariation":1,"fallthrough":{"variation":1},"targets":[]` + extra + `}`
	}
	flags := func(env string) string {
		return fmt.Sprintf(`[
			{"key":"parent","name":"Parent","kind":"boolean","variations":[{"value":true},{"value":false}],"environments":{"%[1]s":%[2]s}},
			{"key":"child","name":"Child","kind":"boolean","variations":[{"value":true},{"value":false}],"environments":{"%[1]s":%[3]s}},
			{"key":"by-kind","name":"By kind","kind":"boolean","variations":[{"value":true},{"value":false}],"environments":{"%[1]s":%[4]s}}]`,
			env, envCfg(""), envCfg(`,"prerequisites":[{"key":"parent","variation":0}]`),
			envCfg(`,"rules":[{"_id":"r","variation":0,"clauses":[{"attribute":"kind","op":"in","values":["org"]}]}]`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "api-test" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"code":"unauthorized","message":"Invalid access token"}`)
			return
		}
		q := r.URL.Query()
		var items string
		switch r.URL.Path {
		case "/api/v2/projects/default/environments":
			items = `[{"key":"test","name":"Test"},{"key":"production","name":"Production"}]`
		case "/api/v2/flags/default":
			items = `[]`
			if q.Get("filter") == "" {
				items = flags(q.Get("env"))
			}
		case "/api/v2/segments/default/test", "/api/v2/segments/default/production":
			items = `[]`
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"code":"not_found","message":"Unknown resource"}`)
			return
		}
		fmt.Fprintf(w, `{"items":%s,"_links":{}}`, items)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ldVars(srv *httptest.Server) map[string]string {
	return map[string]string{"LAUNCHDARKLY_ACCESS_TOKEN": "api-test", "LAUNCHDARKLY_API_URL": srv.URL}
}

func TestImportLaunchDarklyDryRun(t *testing.T) {
	srv := fakeLaunchDarkly(t)
	stdout, stderr, code := run(t, runOpts{Env: ldVars(srv), NoToken: true}, "import", "launchdarkly", "--ld-project", "default")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"LaunchDarkly import: dry run, nothing was written.",
		"production  -> production",
		"Flags     3 read: 2 ready, 1 blocked",
		"it checks the context kind",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestImportLaunchDarklyNeedsAProjectAndToken(t *testing.T) {
	srv := fakeLaunchDarkly(t)
	cases := []struct {
		name string
		vars map[string]string
		args []string
		code int
		msg  string
	}{
		{"no project", ldVars(srv), nil, 2, "--ld-project"},
		{"no token", map[string]string{"LAUNCHDARKLY_API_URL": srv.URL}, []string{"--ld-project", "default"}, 3, "LAUNCHDARKLY_ACCESS_TOKEN"},
		{"refused token", map[string]string{"LAUNCHDARKLY_ACCESS_TOKEN": "wrong", "LAUNCHDARKLY_API_URL": srv.URL}, []string{"--ld-project", "default"}, 3, "refused the access token"},
		{"unknown project", ldVars(srv), []string{"--ld-project", "nope"}, 4, "Unknown resource"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stderr, code := run(t, runOpts{Env: c.vars, NoToken: true}, append([]string{"import", "launchdarkly"}, c.args...)...)
			if code != c.code || !strings.Contains(stderr, c.msg) {
				t.Fatalf("exit %d, stderr %q; want exit %d mentioning %q", code, stderr, c.code, c.msg)
			}
			if strings.Contains(stderr, "wrong") || strings.Contains(stderr, "api-test") {
				t.Fatalf("the token leaked into the error: %q", stderr)
			}
		})
	}
}

func TestImportLaunchDarklyApplyWritesPrerequisitesAfterTheirFlag(t *testing.T) {
	srv := fakeLaunchDarkly(t)
	api := newFakeAPI(t)
	base := "/api/v1/orgs/acme/projects/web"
	api.on("GET", "/api/v1/orgs/acme", 200, `{"id":"o","name":"Acme","slug":"acme","plan":"Solo","limits":{"flagsPerProject":10}}`)
	api.on("GET", base+"/environments", 200, `{"items":[{"id":"e1","key":"dev","name":"Dev","sortOrder":0},{"id":"e2","key":"prod","name":"Prod","sortOrder":1}],"next_cursor":null}`)
	api.on("GET", base+"/flags", 200, `{"items":[],"next_cursor":null}`)
	api.on("GET", base+"/segments", 200, `{"items":[],"next_cursor":null}`)
	api.on("POST", base+"/flags", 201, `{"id":"f1","key":"x","name":"X","type":"Boolean","variations":[{"id":"vt","key":"true","name":"True","value":"true"},{"id":"vf","key":"false","name":"False","value":"false"}]}`)
	api.on("PUT", base+"/flags/child/environments/dev", 204, "")
	api.on("PUT", base+"/flags/child/environments/prod", 204, "")

	_, stderr, code := run(t, runOpts{Env: ldVars(srv), API: api},
		"import", "launchdarkly", "--ld-project", "default", "--org", "acme", "--project", "web",
		"--env-map", "test=dev,production=prod", "--apply")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	creates := api.calls("POST", base+"/flags")
	if len(creates) != 2 || !strings.Contains(creates[0].Body, `"key":"parent"`) || !strings.Contains(creates[1].Body, `"key":"child"`) {
		t.Fatalf("creates = %+v", creates)
	}
	put := api.calls("PUT", base+"/flags/child/environments/prod")
	if len(put) != 1 || !strings.Contains(put[0].Body, `"prerequisiteFlagKey":"parent"`) || !strings.Contains(put[0].Body, `"expectedVariationKey":"true"`) {
		t.Fatalf("env config = %+v", put)
	}
	if got := put[0].Header.Get("X-Intent-Reason"); !strings.Contains(got, "LaunchDarkly") {
		t.Errorf("change reason = %q", got)
	}
}
