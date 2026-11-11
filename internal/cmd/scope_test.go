package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
)

func testSession(t *testing.T, opts runOpts, g *globals) *session {
	t.Helper()
	env, _, _ := newTestEnv(t, opts)
	s, err := newSession(env, g)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOrgAutoResolvesSingle(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/orgs", 200, oneOrg)
	org, err := testSession(t, runOpts{API: api}, &globals{}).Org(context.Background())
	if err != nil || org != "acme" {
		t.Fatalf("org %q err %v", org, err)
	}
}

func TestOrgAmbiguousListsSlugs(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/orgs", 200, `{"items":[{"slug":"acme"},{"slug":"globex"}],"next_cursor":null}`)
	_, err := testSession(t, runOpts{API: api}, &globals{}).Org(context.Background())
	var u UsageError
	if !errors.As(err, &u) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"2 organizations", "--org", "FEATUREFLIP_ORG", "acme, globex"} {
		if !strings.Contains(u.Msg, want) {
			t.Errorf("message %q missing %q", u.Msg, want)
		}
	}
}

func TestOrgNoneVisibleIsAuth(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/orgs", 200, `{"items":[],"next_cursor":null}`)
	_, err := testSession(t, runOpts{API: api}, &globals{}).Org(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no organizations") {
		t.Fatalf("err = %v", err)
	}
	if code := exitcode.Classify(err); code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
}

func TestOrgFlagSkipsLookup(t *testing.T) {
	api := newFakeAPI(t)
	org, err := testSession(t, runOpts{API: api, Env: map[string]string{"FEATUREFLIP_ORG": "env-org"}}, &globals{org: "flag-org"}).Org(context.Background())
	if err != nil || org != "flag-org" {
		t.Fatalf("org %q err %v", org, err)
	}
	if n := api.count("GET"); n != 0 {
		t.Errorf("%d lookups", n)
	}
}

func TestOrgEnvBeatsLookup(t *testing.T) {
	api := newFakeAPI(t)
	org, _ := testSession(t, runOpts{API: api, Env: map[string]string{"FEATUREFLIP_ORG": "env-org"}}, &globals{}).Org(context.Background())
	if org != "env-org" {
		t.Fatalf("org %q", org)
	}
}

func TestProjectAutoResolvesSingle(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/orgs/acme/projects", 200, `{"items":[{"id":"p1","key":"web","name":"Web"}],"next_cursor":null}`)
	p, err := testSession(t, runOpts{API: api}, &globals{}).Project(context.Background(), "acme")
	if err != nil || p != "web" {
		t.Fatalf("project %q err %v", p, err)
	}
}

func TestProjectAmbiguousNamesFlagAndEnv(t *testing.T) {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/orgs/acme/projects", 200, `{"items":[{"key":"web"},{"key":"api"}],"next_cursor":null}`)
	_, err := testSession(t, runOpts{API: api}, &globals{}).Project(context.Background(), "acme")
	var u UsageError
	if !errors.As(err, &u) || !strings.Contains(u.Msg, "--project") || !strings.Contains(u.Msg, "FEATUREFLIP_PROJECT") || !strings.Contains(u.Msg, "web, api") {
		t.Fatalf("err = %v", err)
	}
}
