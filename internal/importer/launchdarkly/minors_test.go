package launchdarkly

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// oneEnvSnapshot is a project with one environment and the given flags.
func oneEnvSnapshot(flags ...flag) *snapshot {
	return &snapshot{
		Environments: []environment{{Key: "production"}},
		Flags:        flags,
		Segments:     map[string][]segment{},
	}
}

func boolFlag(key string, cfg *flagEnv) flag {
	one := 1
	if cfg.OffVariation == nil {
		cfg.OffVariation = &one
	}
	if cfg.Fallthrough.Variation == nil && cfg.Fallthrough.Rollout == nil {
		cfg.Fallthrough.Variation = &one
	}
	return flag{
		Key: key, Kind: "boolean",
		Variations:   []variation{{Value: json.RawMessage("true")}, {Value: json.RawMessage("false")}},
		Environments: map[string]*flagEnv{"production": cfg},
	}
}

func translateSnap(t *testing.T, snap *snapshot) *im.Result {
	t.Helper()
	res, err := translate(snap, im.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res.Summarize()
	return res
}

func TestAnArchivedFlagDoesntCollideWithALiveOne(t *testing.T) {
	archived := boolFlag("New.Checkout", &flagEnv{On: true})
	archived.Archived = true
	res := translateSnap(t, oneEnvSnapshot(archived, boolFlag("new-checkout", &flagEnv{On: true})))
	if is := issue(res, "new-checkout", "key-collision"); is != nil {
		t.Fatalf("false collision with an archived flag: %+v", is)
	}
}

func TestSplitChecksTheRemainderEveryOtherVariationCouldGet(t *testing.T) {
	ws := []weighted{{0, 1000}, {1, 24750}, {2, 24750}, {3, 24750}, {4, 24750}}
	// Featureflip would serve 1 / 24 / 24 / 24 / 27: the remainder lands
	// two points off LaunchDarkly's 25.
	if _, _, _, ok := split(ws, 5, -1); ok {
		t.Fatal("a split that puts a variation 2 points off was accepted")
	}
}

func TestNumericComparisonsWarnAboutLooseMatching(t *testing.T) {
	rule := flagRule{Variation: intp(0), Clauses: []clause{{Attribute: "age", Op: "greaterThan", Values: []json.RawMessage{json.RawMessage("18")}}}}
	res := translateSnap(t, oneEnvSnapshot(boolFlag("adults", &flagEnv{On: true, Rules: []flagRule{rule}})))
	mustIssue(t, res, "", "case-insensitive", im.Warning)
}

func TestCatchAllRolloutWarnsWhereUnbucketableContextsGo(t *testing.T) {
	is := mustIssue(t, translated(t), "fallthrough-rollout", "unbucketable-default", im.Warning)
	if !strings.Contains(is.Message, `"false"`) || !strings.Contains(is.Message, `"true"`) {
		t.Fatalf("message = %q", is.Message)
	}
}

func TestATargetOnAMissingVariationIsAnError(t *testing.T) {
	cfg := &flagEnv{On: true, Targets: []target{{Values: []string{"u1"}, Variation: 5}}}
	res := translateSnap(t, oneEnvSnapshot(boolFlag("bad-target", cfg)))
	mustIssue(t, res, "bad-target", "unreadable-value", im.Error)
}

func TestRateLimitWaitStopsWhenTheContextEnds(t *testing.T) {
	f := newFakeLD(t)
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
		return true
	}
	src := f.source(nil)
	src.Sleep = nil // the real, context-aware wait
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := src.fetch(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("waited %s after the context ended", time.Since(start))
	}
}

func TestPaginationStopsOnANextLinkWithItsQueryReordered(t *testing.T) {
	f := newFakeLD(t)
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v2/flags/web" || r.URL.Query().Get("env") != "test" {
			return false
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items": []any{map[string]any{"key": "a", "kind": "boolean"}},
			// The page just requested, its query reordered.
			"_links": map[string]any{"next": map[string]any{"href": "/api/v2/flags/web?summary=0&limit=50&env=test"}},
		})
		return true
	}
	_, err := f.source(nil).fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("err = %v", err)
	}
	pages := 0
	for _, r := range f.requests {
		if r.URL.Query().Get("env") == "test" {
			pages++
		}
	}
	if pages != 1 {
		t.Fatalf("requested the same page %d times before stopping", pages)
	}
}
