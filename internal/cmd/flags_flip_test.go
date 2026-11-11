package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	prodEnvPath = envsPath + "/production"
	prodCfgPath = flagsPath + "/new-checkout/environments/production"
	togglePath  = prodCfgPath + "/toggle"
)

// flipAPI serves production with the given guards and the flag's current state.
func flipAPI(t *testing.T, enabled, confirm, reason bool) *fakeAPI {
	t.Helper()
	api := newFakeAPI(t)
	api.on("GET", prodEnvPath, 200, fmt.Sprintf(
		`{"id":"e2","key":"production","name":"Production","sortOrder":1,"requireChangeReason":%t,"requireConfirmation":%t}`, reason, confirm))
	api.on("GET", prodCfgPath, 200, fmt.Sprintf(
		`{"environmentId":"e2","environmentKey":"production","isEnabled":%t,"defaultVariationId":"v1","strategy":"SingleVariation","prerequisites":[]}`, enabled))
	api.on("POST", togglePath, 204, "")
	return api
}

func TestFlipOnChangesState(t *testing.T) {
	api := flipAPI(t, false, false, false)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	posts := api.calls("POST", togglePath)
	if len(posts) != 1 || posts[0].Body != `{"enabled":true}` {
		t.Fatalf("posts = %+v", posts)
	}
	if strings.TrimSpace(out) != "new-checkout: off → on in production" {
		t.Errorf("stdout = %q", out)
	}
}

func TestFlipOffChangesState(t *testing.T) {
	api := flipAPI(t, true, false, false)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "off", "new-checkout", "--env", "production")
	if code != 0 || api.calls("POST", togglePath)[0].Body != `{"enabled":false}` {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(out) != "new-checkout: on → off in production" {
		t.Errorf("stdout = %q", out)
	}
}

func TestFlipAlreadyOnIsNoop(t *testing.T) {
	api := flipAPI(t, true, true, true) // guards must not matter for a no-op
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if api.count("POST") != 0 {
		t.Error("sent a toggle for a no-op")
	}
	if strings.TrimSpace(out) != "new-checkout is already on in production" {
		t.Errorf("stdout = %q", out)
	}
}

func TestFlipNoopJSON(t *testing.T) {
	api := flipAPI(t, true, false, false)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"flag": "new-checkout", "environment": "production", "previous": true, "current": true, "changed": false}
	for k, v := range want {
		if doc[k] != v {
			t.Errorf("%s = %v, want %v", k, doc[k], v)
		}
	}
}

func TestFlipChangeJSON(t *testing.T) {
	api := flipAPI(t, false, false, false)
	out, _, _ := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production", "--json")
	var doc map[string]any
	json.Unmarshal([]byte(out), &doc)
	if doc["previous"] != false || doc["current"] != true || doc["changed"] != true {
		t.Errorf("doc = %v", doc)
	}
}

func TestFlipRequiresReason(t *testing.T) {
	api := flipAPI(t, false, false, true)
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if api.count("POST") != 0 {
		t.Error("sent a toggle without the required reason")
	}
	if !strings.Contains(errOut, "production requires a change reason") || !strings.Contains(errOut, "--reason") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestFlipSendsReasonHeader(t *testing.T) {
	api := flipAPI(t, false, false, true)
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production", "--reason", "launch day")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := api.calls("POST", togglePath)[0].Header.Get("X-Intent-Reason"); got != "launch%20day" {
		t.Errorf("X-Intent-Reason = %q", got)
	}
}

func TestFlipServerReasonRequired(t *testing.T) {
	api := flipAPI(t, false, false, false) // setting changed between our read and the write
	api.on("POST", togglePath, 400, `{"error":"change_reason_required","message":"This environment requires a change reason.","docs_url":"d"}`)
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--reason") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestFlipConfirmInteractiveMatches(t *testing.T) {
	api := flipAPI(t, false, true, false)
	term := &fakeTerminal{interactive: true, answers: []string{" production "}}
	_, _, code := run(t, runOpts{API: api, Env: scoped, Terminal: term}, "flags", "on", "new-checkout", "--env", "production")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(term.prompts) != 1 || term.prompts[0] != `Type "production" to turn new-checkout on: ` {
		t.Errorf("prompts = %q", term.prompts)
	}
	if api.count("POST") != 1 {
		t.Error("no toggle after a matching confirmation")
	}
}

func TestFlipConfirmInteractiveMismatch(t *testing.T) {
	api := flipAPI(t, false, true, false)
	term := &fakeTerminal{interactive: true, answers: []string{"prod"}}
	_, errOut, code := run(t, runOpts{API: api, Env: scoped, Terminal: term}, "flags", "on", "new-checkout", "--env", "production")
	if code != 6 {
		t.Fatalf("exit %d, want 6", code)
	}
	if api.count("POST") != 0 || !strings.Contains(errOut, "confirmation did not match; nothing changed") {
		t.Errorf("posts %d stderr %q", api.count("POST"), errOut)
	}
}

func TestFlipConfirmNonInteractiveNeedsYes(t *testing.T) {
	api := flipAPI(t, false, true, false)
	term := &fakeTerminal{interactive: false, answers: []string{"production"}}
	_, errOut, code := run(t, runOpts{API: api, Env: scoped, Terminal: term}, "flags", "on", "new-checkout", "--env", "production")
	if code != 6 {
		t.Fatalf("exit %d, want 6", code)
	}
	if api.count("POST") != 0 || len(term.prompts) != 0 {
		t.Error("prompted or toggled without a terminal")
	}
	if !strings.Contains(errOut, "production requires confirmation") || !strings.Contains(errOut, "--yes") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestFlipConfirmYesSkipsPrompt(t *testing.T) {
	api := flipAPI(t, false, true, false)
	term := &fakeTerminal{interactive: true}
	_, _, code := run(t, runOpts{API: api, Env: scoped, Terminal: term}, "flags", "on", "new-checkout", "--env", "production", "--yes")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(term.prompts) != 0 || api.count("POST") != 1 {
		t.Errorf("prompts %v posts %d", term.prompts, api.count("POST"))
	}
}

func TestFlipMissingEnvFlag(t *testing.T) {
	_, _, code := run(t, runOpts{Env: scoped}, "flags", "on", "new-checkout")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestFlipRetries503(t *testing.T) {
	api := flipAPI(t, false, false, false)
	var n int32
	api.handle("POST", togglePath, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production")
	if code != 0 || n != 2 {
		t.Fatalf("exit %d after %d attempts", code, n)
	}
}

func TestFlipRetries502BecauseItSetsState(t *testing.T) {
	api := flipAPI(t, false, false, false)
	var n int32
	api.handle("POST", togglePath, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(204)
	})
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "production")
	if code != 0 || n != 2 {
		t.Fatalf("exit %d after %d attempts", code, n)
	}
}

func TestFlipEscapesEnvironmentKey(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath+"/my%20env", 200, `{"id":"e3","key":"my env","name":"Mine","sortOrder":2,"requireChangeReason":false,"requireConfirmation":false}`)
	api.on("GET", flagsPath+"/new-checkout/environments/my%20env", 200, `{"environmentId":"e3","environmentKey":"my env","isEnabled":false,"defaultVariationId":"v1","strategy":"SingleVariation","prerequisites":[]}`)
	api.on("POST", flagsPath+"/new-checkout/environments/my%20env/toggle", 204, "")
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "on", "new-checkout", "--env", "my env")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestEmptyAndDotKeysAreUsageErrors(t *testing.T) {
	cases := [][]string{
		{"flags", "on", "new-checkout", "--env", ""},
		{"flags", "on", "new-checkout", "--env", ".."},
		{"flags", "off", "", "--env", "production"},
		{"flags", "on", ".", "--env", "production"},
		{"flags", "status", ""},
		{"flags", "get", ".."},
		{"flags", "create", ""},
		{"flags", "archive", "."},
		{"flags", "restore", ""},
		{"flags", "list", "--env", ".."},
	}
	for _, args := range cases {
		api := newFakeAPI(t)
		_, errOut, code := run(t, runOpts{API: api, Env: scoped}, args...)
		if code != 2 || api.count("GET")+api.count("POST") != 0 {
			t.Errorf("%q: exit %d, %d requests, stderr %q", args, code, api.count("GET")+api.count("POST"), errOut)
		}
	}
}
