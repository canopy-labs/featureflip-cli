package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/canopy-labs/featureflip-cli/internal/config"
)

const oneOrg = `{"items":[{"id":"o1","name":"Acme","slug":"acme","plan":"Team","isActive":true,"createdAt":"2026-01-01T00:00:00Z"}],"next_cursor":null}`

func TestWhoamiUser(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 200, `{"type":"user","id":"u1","name":"Jo Dev","email":"jo@example.com","emailVerified":true}`)
	api.on("GET", "/api/v1/orgs", 200, oneOrg)
	out, _, code := run(t, runOpts{API: api}, "whoami")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"Type: user", "Name: Jo Dev", "Email: jo@example.com", "Organization: acme"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWhoamiServiceTokenScoped(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 200, `{"type":"service_token","id":"s1","name":"ci-bot","organizationId":"o1","role":"Member","projectScope":["p1","p2"]}`)
	api.on("GET", "/api/v1/orgs", 200, oneOrg)
	out, _, code := run(t, runOpts{API: api}, "whoami")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"Type: service token", "Name: ci-bot", "Role: Member", "Projects: 2 project(s)", "Organization: acme"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWhoamiJSON(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 200, `{"type":"user","id":"u1","name":"Jo","email":"jo@example.com"}`)
	api.on("GET", "/api/v1/orgs", 200, oneOrg)
	out, _, code := run(t, runOpts{API: api}, "whoami", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["email"] != "jo@example.com" || got["organization"] != "acme" {
		t.Errorf("json = %v", got)
	}
}

func TestWhoamiNoToken(t *testing.T) {
	_, errOut, code := run(t, runOpts{NoToken: true}, "whoami")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if !strings.Contains(errOut, "featureflip login") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLoginPipedStdinSavesToken(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 200, `{"type":"user","id":"u1","name":"Jo"}`)
	dir := t.TempDir()
	out, _, code := run(t, runOpts{API: api, NoToken: true, ConfigDir: dir, Stdin: "ffp_new_token\n"}, "login", "--org", "acme")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := api.calls("GET", "/api/v1/me")[0].Header.Get("Authorization"); got != "Bearer ffp_new_token" {
		t.Errorf("validated with %q", got)
	}
	f, _, err := config.Load(dir)
	if err != nil || f.Token != "ffp_new_token" || f.Org != "acme" {
		t.Fatalf("saved %+v %v", f, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(config.Path(dir))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
	if !strings.Contains(out, "Logged in as Jo.") || !strings.Contains(out, filepath.Join("featureflip", "config.json")) {
		t.Errorf("stdout = %q", out)
	}
}

func TestLoginInteractiveUsesSecretPrompt(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 200, `{"type":"user","id":"u1","name":"Jo"}`)
	dir := t.TempDir()
	term := &fakeTerminal{interactive: true, answers: []string{"ffp_typed"}}
	_, _, code := run(t, runOpts{API: api, NoToken: true, ConfigDir: dir, Terminal: term}, "login")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(term.prompts) != 1 || !strings.Contains(term.prompts[0], "Paste your Featureflip token") {
		t.Errorf("prompts = %v", term.prompts)
	}
	if f, _, _ := config.Load(dir); f.Token != "ffp_typed" {
		t.Errorf("saved %+v", f)
	}
}

func TestLoginEmptyTokenIsUsageError(t *testing.T) {
	_, _, code := run(t, runOpts{NoToken: true, Stdin: "  \n"}, "login")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestLoginRejectedTokenNotSaved(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 401, `{"error":"unauthorized","message":"Invalid token.","docs_url":"d"}`)
	dir := t.TempDir()
	_, _, code := run(t, runOpts{API: api, NoToken: true, ConfigDir: dir, Stdin: "ffp_bad\n"}, "login")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if _, err := os.Stat(config.Path(dir)); !os.IsNotExist(err) {
		t.Errorf("config written: %v", err)
	}
}

func TestLoginWarnsWhenEnvTokenSet(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 200, `{"type":"user","id":"u1","name":"Jo"}`)
	_, errOut, code := run(t, runOpts{API: api, Stdin: "ffp_new\n"}, "login")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut, "FEATUREFLIP_TOKEN is set and takes precedence") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLogoutKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := config.Save(dir, config.File{Token: "ffp_x", Org: "acme", Project: "web"}); err != nil {
		t.Fatal(err)
	}
	out, _, code := run(t, runOpts{ConfigDir: dir, NoToken: true}, "logout")
	if code != 0 || !strings.Contains(out, "Logged out.") {
		t.Fatalf("exit %d out %q", code, out)
	}
	f, _, _ := config.Load(dir)
	if f.Token != "" || f.Org != "acme" || f.Project != "web" {
		t.Errorf("after logout %+v", f)
	}
}

func TestLogoutWithoutConfigSucceeds(t *testing.T) {
	_, _, code := run(t, runOpts{NoToken: true}, "logout")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestTokenNeverPrinted(t *testing.T) {
	const canary = "ffp_leak_canary_123"
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 401, `{"error":"unauthorized","message":"Invalid token.","docs_url":"d"}`)
	for _, args := range [][]string{{"whoami"}, {"whoami", "--json"}, {"login"}, {"login", "--json"}} {
		opts := runOpts{API: api, Env: map[string]string{"FEATUREFLIP_TOKEN": canary}, Stdin: canary + "\n"}
		out, errOut, _ := run(t, opts, args...)
		if strings.Contains(out+errOut, canary) {
			t.Errorf("%v leaked the token:\nstdout %q\nstderr %q", args, out, errOut)
		}
	}
}
