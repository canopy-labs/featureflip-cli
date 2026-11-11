package cmd

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const createdFlag = `{"id":"f1","key":"new-checkout","name":"new-checkout","type":"Boolean","isArchived":false,"clientSideVisible":false,"tags":[],"variations":[]}`

func TestCreateBooleanDefaults(t *testing.T) {
	api := newFakeAPI(t)
	api.on("POST", flagsPath, 201, createdFlag)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "create", "new-checkout")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	req := api.calls("POST", flagsPath)[0]
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"key": "new-checkout", "name": "new-checkout", "type": "Boolean", "clientSideVisible": false}
	if len(body) != len(want) {
		t.Errorf("body = %v", body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	if k := req.Header.Get("Idempotency-Key"); !uuidV4.MatchString(k) {
		t.Errorf("Idempotency-Key = %q", k)
	}
	if strings.TrimSpace(out) != "Created new-checkout (Boolean). It is off in every environment." {
		t.Errorf("stdout = %q", out)
	}
}

func TestCreateStringWithVariationsAndOptions(t *testing.T) {
	api := newFakeAPI(t)
	api.on("POST", flagsPath, 201, createdFlag)
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "create", "theme",
		"--type", "STRING", "--name", "Theme", "--description", "UI theme",
		"--variation", "dark=Dark mode", "--variation", "light=a=b", "--tag", "ui", "--tag", "beta", "--client-side")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var body struct {
		Key, Name, Type, Description string
		Tags                         []string
		ClientSideVisible            bool
		InitialVariations            []struct{ Key, Name, Value string }
	}
	json.Unmarshal([]byte(api.calls("POST", flagsPath)[0].Body), &body)
	if body.Type != "String" || body.Name != "Theme" || body.Description != "UI theme" || !body.ClientSideVisible ||
		strings.Join(body.Tags, ",") != "ui,beta" || len(body.InitialVariations) != 2 {
		t.Fatalf("body = %+v", body)
	}
	if v := body.InitialVariations[1]; v.Key != "light" || v.Name != "light" || v.Value != "a=b" {
		t.Errorf("variation split on the first '=' only: %+v", v)
	}
}

func TestCreateStringNeedsTwoVariations(t *testing.T) {
	api := newFakeAPI(t)
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "create", "theme", "--type", "string", "--variation", "dark=d")
	if code != 2 || !strings.Contains(errOut, "--type string needs at least two --variation key=value") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	if api.count("POST") != 0 {
		t.Error("sent a create")
	}
}

func TestCreateBooleanRejectsVariation(t *testing.T) {
	_, _, code := run(t, runOpts{Env: scoped}, "flags", "create", "x", "--variation", "a=b")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestCreateRejectsUnknownTypeAndBadVariation(t *testing.T) {
	if _, _, code := run(t, runOpts{Env: scoped}, "flags", "create", "x", "--type", "date"); code != 2 {
		t.Errorf("--type date: exit %d", code)
	}
	if _, _, code := run(t, runOpts{Env: scoped}, "flags", "create", "x", "--type", "number", "--variation", "one", "--variation", "two=2"); code != 2 {
		t.Errorf("variation without '=': exit %d", code)
	}
}

func TestCreateRetries502WithSameKey(t *testing.T) {
	api := newFakeAPI(t)
	var n int32
	api.handle("POST", flagsPath, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(createdFlag))
	})
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "create", "new-checkout")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := api.calls("POST", flagsPath)
	if len(calls) != 2 || calls[0].Header.Get("Idempotency-Key") != calls[1].Header.Get("Idempotency-Key") {
		t.Errorf("calls = %d, keys differ or missing", len(calls))
	}
}

func TestCreatePlanLimit(t *testing.T) {
	api := newFakeAPI(t)
	api.on("POST", flagsPath, 403, `{"error":"plan_limit_reached","message":"Solo allows 10 flags per project.","docs_url":"d","plan_limit":{"limit":"flags","plan":"Solo","max":10}}`)
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "create", "x", "--json")
	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	var doc map[string]any
	json.Unmarshal([]byte(out), &doc)
	if doc["error"] != "plan_limit_reached" || doc["plan_limit"] == nil {
		t.Errorf("doc = %v", doc)
	}
}

func TestCreateJSONPrintsCreatedFlag(t *testing.T) {
	api := newFakeAPI(t)
	api.on("POST", flagsPath, 201, createdFlag)
	out, _, _ := run(t, runOpts{API: api, Env: scoped}, "flags", "create", "new-checkout", "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["id"] != "f1" {
		t.Errorf("stdout = %q", out)
	}
}

const (
	archivePath = flagsPath + "/new-checkout/archive"
	restorePath = flagsPath + "/new-checkout/restore"
	guardedEnvs = `{"items":[
		{"id":"e1","key":"development","name":"Development","sortOrder":0,"requireChangeReason":false,"requireConfirmation":false},
		{"id":"e2","key":"production","name":"Production","sortOrder":1,"requireChangeReason":true,"requireConfirmation":true}],"next_cursor":null}`
)

func TestArchiveNeedsReasonWhenAnyEnvRequires(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, guardedEnvs)
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "archive", "new-checkout")
	if code != 2 || !strings.Contains(errOut, "this project requires a change reason") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	if api.count("POST") != 0 {
		t.Error("archived without a reason")
	}
}

func TestArchiveSendsReasonAndNoForceByDefault(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, guardedEnvs)
	api.on("POST", archivePath, 204, "")
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "archive", "new-checkout", "--reason", "cleanup")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	req := api.calls("POST", archivePath)[0]
	if req.Header.Get("X-Intent-Reason") != "cleanup" || req.Query.Has("force") {
		t.Errorf("request = %+v", req)
	}
	if strings.TrimSpace(out) != "Archived new-checkout." {
		t.Errorf("stdout = %q", out)
	}
}

func TestArchiveForceQuery(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	api.on("POST", archivePath, 204, "")
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "archive", "new-checkout", "--force")
	if code != 0 || api.calls("POST", archivePath)[0].Query.Get("force") != "true" {
		t.Fatalf("exit %d", code)
	}
}

func TestArchiveRecentlyEvaluatedHint(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	// The exact envelope ArchiveFeatureFlagCommandHandler + PublicApiExceptionFilter produce.
	api.on("POST", archivePath, 400, `{"error":"validation_failed","message":"One or more fields are invalid.","docs_url":"d","fields":{"flag":["FLAG_RECENTLY_EVALUATED: production,staging"]}}`)
	_, errOut, code := run(t, runOpts{API: api, Env: scoped}, "flags", "archive", "new-checkout")
	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if !strings.Contains(errOut, "Still evaluated in: production, staging. Pass --force to archive anyway.") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestArchiveJSON(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, twoEnvs)
	api.on("POST", archivePath, 204, "")
	out, _, _ := run(t, runOpts{API: api, Env: scoped}, "flags", "archive", "new-checkout", "--json")
	var doc map[string]any
	if json.Unmarshal([]byte(out), &doc); doc["flag"] != "new-checkout" || doc["archived"] != true {
		t.Errorf("stdout = %q", out)
	}
}

func TestRestoreSendsReason(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, guardedEnvs)
	api.on("POST", restorePath, 204, "")
	out, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "restore", "new-checkout", "--reason", "needed again")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := api.calls("POST", restorePath)[0].Header.Get("X-Intent-Reason"); got != "needed%20again" {
		t.Errorf("X-Intent-Reason = %q", got)
	}
	if strings.TrimSpace(out) != "Restored new-checkout." {
		t.Errorf("stdout = %q", out)
	}
}

func TestRestoreNeedsReasonWhenAnyEnvRequires(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", envsPath, 200, guardedEnvs)
	_, _, code := run(t, runOpts{API: api, Env: scoped}, "flags", "restore", "new-checkout")
	if code != 2 || api.count("POST") != 0 {
		t.Fatalf("exit %d posts %d", code, api.count("POST"))
	}
}
