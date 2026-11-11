package cmd

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

const (
	flagsPath   = "/api/v1/orgs/acme/projects/web/flags"
	comparePath = "/api/v1/orgs/acme/projects/web/compare"
	// Two environments, served out of display order to prove sorting.
	twoEnvs = `{"items":[
		{"id":"e2","key":"production","name":"Production","sortOrder":1,"requireChangeReason":false,"requireConfirmation":false},
		{"id":"e1","key":"development","name":"Development","sortOrder":0,"requireChangeReason":false,"requireConfirmation":false}],"next_cursor":null}`
)

func flagItem(key, typ string) string {
	return `{"id":"id-` + key + `","key":"` + key + `","name":"Name ` + key + `","type":"` + typ + `","isArchived":false,"clientSideVisible":false,"tags":["t1","t2"],"createdAt":"x","updatedAt":"x","isPermanent":false}`
}

func TestFlagsListTwoPages(t *testing.T) {
	api := newFakeAPI(t)
	api.handle("GET", flagsPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			w.Write([]byte(`{"items":[` + flagItem("a-flag", "Boolean") + `],"next_cursor":"p2"}`))
			return
		}
		w.Write([]byte(`{"items":[` + flagItem("b-flag", "String") + `],"next_cursor":null}`))
	})
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "KEY") || !strings.Contains(lines[0], "TYPE") || !strings.Contains(lines[0], "TAGS") {
		t.Fatalf("table:\n%s", out)
	}
	if !strings.Contains(lines[1], "a-flag") || !strings.Contains(lines[1], "t1,t2") || !strings.Contains(lines[2], "b-flag") {
		t.Errorf("rows:\n%s", out)
	}
}

func TestFlagsListWithEnvUsesCompareSameEnv(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", flagsPath, 200, `{"items":[`+flagItem("a-flag", "Boolean")+`,`+flagItem("b-flag", "Boolean")+`],"next_cursor":null}`)
	api.on("GET", comparePath, 200, `{"items":[
		{"flagKey":"a-flag","flagName":"A","base":{"isEnabled":true,"ruleCount":0},"other":{"isEnabled":true,"ruleCount":0}},
		{"flagKey":"b-flag","flagName":"B","base":{"isEnabled":false,"ruleCount":0},"other":{"isEnabled":false,"ruleCount":0}}],"next_cursor":null}`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "list", "--env", "production")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := api.calls("GET", comparePath)
	if len(calls) != 1 || calls[0].Query.Get("base") != "production" || calls[0].Query.Get("other") != "production" {
		t.Fatalf("compare calls = %+v", calls)
	}
	if n := api.count("GET"); n != 2 {
		t.Errorf("%d GETs, want 2 (no per-flag requests)", n)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], "PRODUCTION") || !strings.HasSuffix(strings.TrimSpace(lines[1]), "on") || !strings.HasSuffix(strings.TrimSpace(lines[2]), "off") {
		t.Errorf("table:\n%s", out)
	}
}

func TestFlagsListEnvMissingSideShowsDash(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", flagsPath, 200, `{"items":[`+flagItem("a-flag", "Boolean")+`,`+flagItem("b-flag", "Boolean")+`],"next_cursor":null}`)
	api.on("GET", comparePath, 200, `{"items":[{"flagKey":"a-flag","flagName":"A","base":null,"other":null}],"next_cursor":null}`)
	out, _, _ := run(t, runOpts{API: api, Env: scoped}, "flags", "list", "--env", "production")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("table:\n%s", out)
	}
	for _, l := range lines[1:] {
		if !strings.HasSuffix(strings.TrimSpace(l), "-") {
			t.Errorf("row %q should end in -", l)
		}
	}
}

func TestFlagsListArchivedIgnoresEnv(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", flagsPath, 200, `{"items":[`+flagItem("old", "Boolean")+`],"next_cursor":null}`)
	out, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "list", "--archived", "--env", "production")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(api.calls("GET", comparePath)) != 0 || strings.Contains(out, "PRODUCTION") {
		t.Errorf("compare used with --archived:\n%s", out)
	}
	if !strings.Contains(errOut, "--env is ignored with --archived") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestFlagsListFilters(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", flagsPath, 200, `{"items":[],"next_cursor":null}`)
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "list", "--search", "check", "--tag", "ui", "--archived")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	q := api.calls("GET", flagsPath)[0].Query
	if q.Get("search") != "check" || strings.Join(q["tag"], ",") != "ui" || q.Get("archived") != "true" {
		t.Errorf("query = %v", q)
	}
}

// The server returns active AND archived flags when "archived" is omitted,
// so the default listing must ask for active ones explicitly.
func TestFlagsListDefaultsToActiveFlags(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", flagsPath, 200, `{"items":[],"next_cursor":null}`)
	if _, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "list"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if q := api.calls("GET", flagsPath)[0].Query; q.Get("archived") != "false" {
		t.Errorf("query = %v, want archived=false", q)
	}
}

// The server filters by one tag; a second would be silently ignored.
func TestFlagsListRejectsRepeatedTag(t *testing.T) {
	api := newFakeAPI(t)
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "list", "--tag", "ui", "--tag", "beta")
	if code != 2 || !strings.Contains(errOut, "--tag") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	if api.count("GET") != 0 {
		t.Error("sent a request")
	}
}

func TestFlagsListJSONWithEnvAddsEnabled(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", flagsPath, 200, `{"items":[`+flagItem("a-flag", "Boolean")+`,`+flagItem("b-flag", "Boolean")+`],"next_cursor":null}`)
	api.on("GET", comparePath, 200, `{"items":[{"flagKey":"a-flag","flagName":"A","base":{"isEnabled":true,"ruleCount":0},"other":null}],"next_cursor":null}`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "list", "--env", "production", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatal(err)
	}
	if items[0]["enabled"] != true || items[1]["enabled"] != nil || items[0]["isPermanent"] != false {
		t.Errorf("items = %v", items)
	}
}

func TestFlagsListColorOnlyAtTerminal(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", flagsPath, 200, `{"items":[`+flagItem("a-flag", "Boolean")+`],"next_cursor":null}`)
	api.on("GET", comparePath, 200, `{"items":[{"flagKey":"a-flag","flagName":"A","base":{"isEnabled":true,"ruleCount":0},"other":null}],"next_cursor":null}`)
	out, _, _ := run(t, runOpts{API: api, Env: scoped, StdoutTTY: true}, "flags", "list", "--env", "production")
	if !strings.Contains(out, "\x1b[32mon") {
		t.Errorf("expected color at a terminal: %q", out)
	}
	out, _, _ = run(t, runOpts{API: api, Env: scoped, StdoutTTY: true}, "flags", "list", "--env", "production", "--no-color")
	if strings.Contains(out, "\x1b[") {
		t.Errorf("--no-color still colored: %q", out)
	}
	// Piped stdout stays plain even when a person is at the keyboard.
	out, _, _ = run(t, runOpts{API: api, Env: scoped, Terminal: &fakeTerminal{interactive: true}}, "flags", "list", "--env", "production")
	if strings.Contains(out, "\x1b[") {
		t.Errorf("colored a piped stdout: %q", out)
	}
	env := map[string]string{"FEATUREFLIP_ORG": "acme", "FEATUREFLIP_PROJECT": "web", "NO_COLOR": "1"}
	out, _, _ = run(t, runOpts{API: api, Env: env, StdoutTTY: true}, "flags", "list", "--env", "production")
	if strings.Contains(out, "\x1b[") {
		t.Errorf("NO_COLOR still colored: %q", out)
	}
}

const flagEnvsPath = flagsPath + "/new-checkout/environments"

func TestFlagsStatusOrdersBySortOrder(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	api.on("GET", flagEnvsPath, 200, `[
		{"environmentId":"e2","environmentKey":"production","isEnabled":false,"defaultVariationId":"v1","strategy":"SingleVariation","prerequisites":[]},
		{"environmentId":"e1","environmentKey":"development","isEnabled":true,"defaultVariationId":"v1","strategy":"SingleVariation","prerequisites":[]}]`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "status", "new-checkout")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "development") || !strings.HasSuffix(lines[1], "on") ||
		!strings.HasPrefix(lines[2], "production") || !strings.HasSuffix(lines[2], "off") {
		t.Errorf("table:\n%s", out)
	}
}

func TestFlagsStatusJSONNullForMissingConfig(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	api.on("GET", flagEnvsPath, 200, `[{"environmentId":"e1","environmentKey":"development","isEnabled":true,"defaultVariationId":"v1","strategy":"SingleVariation","prerequisites":[]}]`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "status", "new-checkout", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var doc struct {
		Flag         string `json:"flag"`
		Environments []struct {
			Key     string `json:"key"`
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"`
		} `json:"environments"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Flag != "new-checkout" || len(doc.Environments) != 2 || doc.Environments[0].Key != "development" ||
		*doc.Environments[0].Enabled != true || doc.Environments[1].Enabled != nil {
		t.Errorf("doc = %s", out)
	}
}

func TestFlagsGetMapsDefaultVariationKey(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	api.on("GET", flagsPath+"/new-checkout", 200, `{"id":"f1","key":"new-checkout","name":"New checkout","description":"Ship it","type":"Boolean","isArchived":false,"clientSideVisible":true,"tags":["ui"],
		"variations":[{"id":"v-true","key":"true","name":"On","value":"true"},{"id":"v-false","key":"false","name":"Off","value":"false"}]}`)
	api.on("GET", flagEnvsPath, 200, `[
		{"environmentId":"e1","environmentKey":"development","isEnabled":true,"defaultVariationId":"v-true","strategy":"SingleVariation","prerequisites":[]},
		{"environmentId":"e2","environmentKey":"production","isEnabled":false,"defaultVariationId":"v-false","strategy":"PercentageRollout","prerequisites":[]}]`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "get", "new-checkout")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"Key: new-checkout", "Name: New checkout", "Type: Boolean", "Description: Ship it", "Tags: ui", "Client-side: yes", "VARIATIONS", "ENVIRONMENTS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	var devRow, prodRow string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "development") {
			devRow = l
		}
		if strings.HasPrefix(l, "production") {
			prodRow = l
		}
	}
	if f := strings.Fields(devRow); len(f) != 4 || f[1] != "true" || f[2] != "SingleVariation" || f[3] != "on" {
		t.Errorf("development row %q", devRow)
	}
	if f := strings.Fields(prodRow); len(f) != 4 || f[1] != "false" || f[2] != "PercentageRollout" || f[3] != "off" {
		t.Errorf("production row %q", prodRow)
	}
}

func TestFlagsGetNotFoundDidYouMean(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	api.on("GET", flagsPath+"/new-chekout", 404, `{"error":"flag_not_found","message":"No flag 'new-chekout'.","docs_url":"d","did_you_mean":["new-checkout"]}`)
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "get", "new-chekout")
	if code != 4 || !strings.Contains(errOut, "Did you mean: new-checkout") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
}

func TestFlagsGetNeedsOneArg(t *testing.T) {
	_, _, code := run(t, runOpts{Env: scoped}, "flags", "get")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// Color escapes count toward a tabwriter column's width, so the colored
// state cell must be the last column or the columns after it drift.
func TestFlagsGetColumnsAlignWithColor(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	api.on("GET", flagsPath+"/new-checkout", 200, `{"id":"f1","key":"new-checkout","name":"N","type":"Boolean","isArchived":false,"clientSideVisible":false,"tags":[],
		"variations":[{"id":"v-true","key":"true","name":"On","value":"true"}]}`)
	api.on("GET", flagEnvsPath, 200, `[{"environmentId":"e1","environmentKey":"development","isEnabled":true,"defaultVariationId":"v-true","strategy":"SingleVariation","prerequisites":[]}]`)
	out, _, code := run(t, runOpts{API: api, Env: scoped, StdoutTTY: true}, "flags", "get", "new-checkout")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(out, "\n")
	var header, row string
	for i, l := range lines {
		if strings.HasPrefix(l, "ENVIRONMENT ") {
			header, row = l, lines[i+1]
		}
	}
	if !strings.Contains(row, "\x1b[") {
		t.Fatalf("expected a colored row: %q", row)
	}
	visible := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(row, "")
	if strings.Index(header, "STRATEGY") != strings.Index(visible, "SingleVariation") {
		t.Errorf("STRATEGY column misaligned:\n%s\n%s", header, visible)
	}
}
