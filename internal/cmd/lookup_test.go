package cmd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// scoped pins org and project so commands skip the lookups.
var scoped = map[string]string{"FEATUREFLIP_ORG": "acme", "FEATUREFLIP_PROJECT": "web"}

const envsPath = "/api/v1/orgs/acme/projects/web/environments"

func TestProjectsListTable(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/orgs/acme/projects", 200, `{"items":[{"id":"p1","key":"web","name":"Web app"},{"id":"p2","key":"api","name":"API"}],"next_cursor":null}`)
	out, _, code := run(t, runOpts{API: api, Env: map[string]string{"FEATUREFLIP_ORG": "acme"}}, "projects", "list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "KEY") || !strings.Contains(lines[0], "NAME") {
		t.Fatalf("table:\n%s", out)
	}
	if !strings.Contains(lines[1], "web") || !strings.Contains(lines[1], "Web app") || !strings.Contains(lines[2], "api") {
		t.Errorf("rows:\n%s", out)
	}
}

func TestEnvironmentsListShowsGuards(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, `{"items":[
		{"id":"e1","key":"development","name":"Development","sortOrder":0,"requireChangeReason":false,"requireConfirmation":false},
		{"id":"e2","key":"production","name":"Production","sortOrder":1,"requireChangeReason":true,"requireConfirmation":true}],"next_cursor":null}`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "environments", "list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], "CONFIRM") || !strings.Contains(lines[0], "REASON") {
		t.Fatalf("header %q", lines[0])
	}
	if strings.Contains(lines[1], "yes") {
		t.Errorf("development row %q should have no guards", lines[1])
	}
	if strings.Count(lines[2], "yes") != 2 {
		t.Errorf("production row %q should show both guards", lines[2])
	}
}

func TestSegmentsListJSON(t *testing.T) {
	api := newFakeAPI(t)
	items := `[{"id":"s1","key":"beta","name":"Beta users","description":null,"conditions":[]}]`
	api.on("GET", "/api/v1/orgs/acme/projects/web/segments", 200, `{"items":`+items+`,"next_cursor":null}`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "segments", "list", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got, want any
	json.Unmarshal([]byte(out), &got)
	json.Unmarshal([]byte(items), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %s", out)
	}
}

func TestJSONErrorOnStdout(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 404, `{"error":"project_not_found","message":"No project 'web'.","docs_url":"https://d","did_you_mean":["webapp"]}`)
	out, errOut, code := run(t, runOpts{API: api, Env: scoped}, "environments", "list", "--json")
	if code != 4 {
		t.Fatalf("exit %d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout not JSON: %q (stderr %q)", out, errOut)
	}
	if doc["error"] != "project_not_found" || doc["status"] != float64(404) || doc["message"] != "No project 'web'." {
		t.Errorf("doc = %v", doc)
	}
	if dym, _ := doc["did_you_mean"].([]any); len(dym) != 1 || dym[0] != "webapp" {
		t.Errorf("did_you_mean = %v", doc["did_you_mean"])
	}
}

func TestJSONErrorForGatewayHTML(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/orgs/acme/projects", 502, `<html>Bad Gateway</html>`)
	out, _, code := run(t, runOpts{API: api, Env: map[string]string{"FEATUREFLIP_ORG": "acme"}}, "projects", "list", "--json")
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout not JSON: %q", out)
	}
	if doc["error"] != "http_502" || doc["status"] != float64(502) {
		t.Errorf("doc = %v", doc)
	}
}

func TestJSONErrorForNoToken(t *testing.T) {
	out, _, code := run(t, runOpts{NoToken: true}, "projects", "list", "--json")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout not JSON: %q", out)
	}
	if doc["error"] != "no_token" || doc["status"] != nil {
		t.Errorf("doc = %v", doc)
	}
}

func TestHuman404Hint(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 404, `{"error":"project_not_found","message":"No project 'web'.","docs_url":"d","did_you_mean":["webapp"]}`)
	out, errOut, code := run(t, runOpts{API: api, Env: scoped}, "environments", "list")
	if code != 4 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	for _, want := range []string{"Error: No project 'web'.", "Did you mean: webapp", "public API isn't enabled", "service token isn't scoped"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}
