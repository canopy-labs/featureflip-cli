package statsig

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// TestApplyToALiveProject writes the recorded Statsig project into a real
// Featureflip project and checks that a second plan finds every written item
// unchanged, which only holds if the API hands back exactly what was written.
// It runs only when pointed at a disposable project whose environments
// include development, staging and production:
//
//	FEATUREFLIP_IMPORT_LIVE_URL=http://localhost:5000 FEATUREFLIP_IMPORT_LIVE_TOKEN=... \
//	FEATUREFLIP_IMPORT_LIVE_ORG=acme FEATUREFLIP_IMPORT_LIVE_PROJECT=scratch go test ./internal/importer/statsig -run Live -v
func TestApplyToALiveProject(t *testing.T) {
	url := os.Getenv("FEATUREFLIP_IMPORT_LIVE_URL")
	if url == "" {
		t.Skip("set FEATUREFLIP_IMPORT_LIVE_URL, _TOKEN, _ORG and _PROJECT to run against a disposable project")
	}
	ctx := context.Background()
	opts := im.ApplyOptions{
		Org: os.Getenv("FEATUREFLIP_IMPORT_LIVE_ORG"), Project: os.Getenv("FEATUREFLIP_IMPORT_LIVE_PROJECT"),
		CarryState: true,
	}
	client := api.New(api.Options{BaseURL: url, Token: os.Getenv("FEATUREFLIP_IMPORT_LIVE_TOKEN"),
		RateLimit: &api.RateLimitPolicy{MaxWait: 2 * time.Minute, Retries: 10, OnWait: func(d time.Duration) { t.Logf("rate limited; waiting %s", d) }}})
	f := newFakeConsole(t, loadSnapshot(t))
	src := &Source{BaseURL: f.srv.URL, Key: "k", Sleep: func(time.Duration) {}}

	load := func() *im.Result {
		res, err := src.Load(ctx, im.Options{})
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
	wrote := 0
	for i, st := range second.Steps {
		before := first.Steps[i]
		if before.Done {
			wrote++
		}
		if before.Done && st.Action != im.ActUnchanged {
			t.Errorf("%s %s was written but now plans as %s %v: %s", st.Kind, st.Key, st.Action, st.Environments, st.Why)
		}
	}
	if wrote == 0 {
		// The project already held this import: everything must read unchanged.
		for _, st := range second.Steps {
			if st.Action != im.ActUnchanged && st.Action != im.ActBlocked {
				t.Errorf("%s %s plans as %s %v: %s", st.Kind, st.Key, st.Action, st.Environments, st.Why)
			}
		}
		t.Log("the project already held this import; everything read back unchanged")
		return
	}
	t.Logf("%d items written and read back unchanged", wrote)
}
