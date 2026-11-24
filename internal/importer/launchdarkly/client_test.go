package launchdarkly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
)

const testToken = "api-0123-secret"

// fakeLD serves a project the way LaunchDarkly's REST API does: every list one
// item per page, linked by _links.next, so paging is exercised everywhere.
type fakeLD struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	envs     []map[string]any
	// flags holds each environment's flag list, as `env=<key>` returns it.
	flags    map[string][]map[string]any
	archived []string
	// segments holds each environment's segment list; single holds what a
	// single-segment GET returns, by env and key.
	segments map[string][]map[string]any
	single   map[string]map[string]any
	// before, when set, answers instead for the requests it returns true for.
	before func(w http.ResponseWriter, r *http.Request) bool
}

func newFakeLD(t *testing.T) *fakeLD {
	t.Helper()
	f := &fakeLD{
		envs: []map[string]any{{"key": "test", "name": "Test"}, {"key": "production", "name": "Production"}},
		flags: map[string][]map[string]any{
			"test": {
				{"key": "checkout", "name": "Checkout", "kind": "boolean", "variations": []any{map[string]any{"value": true}, map[string]any{"value": false}},
					"environments": map[string]any{"test": map[string]any{"on": true, "offVariation": 1, "fallthrough": map[string]any{"variation": 0}}}},
				{"key": "banner", "name": "Banner", "kind": "multivariate", "variations": []any{map[string]any{"value": "a"}, map[string]any{"value": "b"}},
					"environments": map[string]any{"test": map[string]any{"on": false, "offVariation": 0, "fallthrough": map[string]any{"variation": 1}}}},
			},
			"production": {
				{"key": "checkout", "name": "Checkout", "kind": "boolean", "variations": []any{map[string]any{"value": true}, map[string]any{"value": false}},
					"environments": map[string]any{"production": map[string]any{"on": false, "offVariation": 1, "fallthrough": map[string]any{"variation": 1}}}},
				{"key": "banner", "name": "Banner", "kind": "multivariate", "variations": []any{map[string]any{"value": "a"}, map[string]any{"value": "b"}},
					"environments": map[string]any{"production": map[string]any{"on": true, "offVariation": 0, "fallthrough": map[string]any{"variation": 0}}}},
			},
		},
		archived: []string{"old-flag"},
		segments: map[string][]map[string]any{
			"test":       {{"key": "beta", "name": "Beta", "included": []string{"u1"}, "excluded": []string{}, "rules": []any{}}},
			"production": {{"key": "beta", "name": "Beta", "included": []string{}}},
		},
		single: map[string]map[string]any{
			"production/beta": {"key": "beta", "name": "Beta", "included": []string{"u2"}, "rules": []any{}},
		},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(r.Context()))
		before := f.before
		f.mu.Unlock()
		if before != nil && before(w, r) {
			return
		}
		f.serve(t, w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLD) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	list := func(items []map[string]any) {
		page := []map[string]any{}
		if offset < len(items) {
			page = append(page, items[offset])
		}
		links := map[string]any{"self": map[string]any{"href": r.URL.RequestURI()}}
		if offset+1 < len(items) {
			nq := r.URL.Query()
			nq.Set("offset", strconv.Itoa(offset+1))
			nq.Set("limit", "1")
			links["next"] = map[string]any{"href": r.URL.Path + "?" + nq.Encode()}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": page, "totalCount": len(items), "_links": links})
	}
	switch p := r.URL.Path; {
	case p == "/api/v2/projects/web/environments":
		list(f.envs)
	case p == "/api/v2/flags/web" && q.Get("filter") == "state:archived":
		var items []map[string]any
		for _, k := range f.archived {
			items = append(items, map[string]any{"key": k, "archived": true})
		}
		list(items)
	case p == "/api/v2/flags/web":
		list(f.flags[q.Get("env")])
	case strings.HasPrefix(p, "/api/v2/segments/web/"):
		rest := strings.Split(strings.TrimPrefix(p, "/api/v2/segments/web/"), "/")
		if len(rest) == 1 {
			list(f.segments[rest[0]])
			return
		}
		seg, ok := f.single[rest[0]+"/"+rest[1]]
		if !ok {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(seg)
	default:
		t.Errorf("unexpected request %s", r.URL)
		w.WriteHeader(404)
	}
}

func (f *fakeLD) source(sleeps *[]time.Duration) *Source {
	return &Source{
		BaseURL: f.srv.URL, Project: "web", Token: testToken, HTTP: f.srv.Client(),
		Sleep: func(d time.Duration) {
			if sleeps != nil {
				*sleeps = append(*sleeps, d)
			}
		},
	}
}

func fetchFrom(t *testing.T, f *fakeLD) *snapshot {
	t.Helper()
	snap, err := f.source(nil).fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestFetchSendsTheTokenAndAPIVersion(t *testing.T) {
	f := newFakeLD(t)
	fetchFrom(t, f)
	if len(f.requests) == 0 {
		t.Fatal("no requests")
	}
	for _, r := range f.requests {
		if got := r.Header.Get("Authorization"); got != testToken {
			t.Fatalf("%s Authorization = %q", r.URL, got)
		}
		if got := r.Header.Get("LD-API-Version"); got != "20240415" {
			t.Fatalf("%s LD-API-Version = %q", r.URL, got)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "featureflip-cli") {
			t.Fatalf("%s User-Agent = %q", r.URL, r.Header.Get("User-Agent"))
		}
	}
}

func TestFetchMergesEachEnvironmentsFlags(t *testing.T) {
	f := newFakeLD(t)
	snap := fetchFrom(t, f)
	if len(snap.Environments) != 2 || snap.Environments[1].Key != "production" {
		t.Fatalf("environments = %+v", snap.Environments)
	}
	if len(snap.Flags) != 2 || snap.Flags[0].Key != "checkout" || snap.Flags[1].Key != "banner" {
		t.Fatalf("flags = %+v", snap.Flags)
	}
	co := snap.Flags[0]
	if co.Environments["test"] == nil || co.Environments["production"] == nil {
		t.Fatalf("checkout environments = %+v", co.Environments)
	}
	if !co.Environments["test"].On || co.Environments["production"].On {
		t.Fatalf("checkout on/off = %v/%v", co.Environments["test"].On, co.Environments["production"].On)
	}
	for _, r := range f.requests {
		if r.URL.Path == "/api/v2/flags/web" && r.URL.Query().Get("filter") == "" {
			if r.URL.Query().Get("summary") != "0" || r.URL.Query().Get("env") == "" {
				t.Fatalf("flags request %s lacks summary=0 or env", r.URL)
			}
		}
	}
}

func TestFetchFollowsNextLinks(t *testing.T) {
	f := newFakeLD(t)
	snap := fetchFrom(t, f)
	pages := 0
	for _, r := range f.requests {
		if r.URL.Query().Get("env") == "test" {
			pages++
		}
	}
	if pages != 2 || len(snap.Flags) != 2 {
		t.Fatalf("read %d pages, %d flags", pages, len(snap.Flags))
	}
}

func TestPaginationStopsOnARepeatedNextLink(t *testing.T) {
	f := newFakeLD(t)
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v2/projects/web/environments" {
			return false
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items":  []any{map[string]any{"key": "test"}},
			"_links": map[string]any{"next": map[string]any{"href": "/api/v2/projects/web/environments?limit=1&offset=1"}},
		})
		return true
	}
	_, err := f.source(nil).fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "/api/v2/projects/web/environments") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchReadsArchivedFlagKeys(t *testing.T) {
	snap := fetchFrom(t, newFakeLD(t))
	if strings.Join(snap.Archived, ",") != "old-flag" {
		t.Fatalf("archived = %v", snap.Archived)
	}
}

func TestFetchFetchesSegmentsSinglyWhenTheListOmitsRules(t *testing.T) {
	f := newFakeLD(t)
	snap := fetchFrom(t, f)
	if got := snap.Segments["test"]; len(got) != 1 || strings.Join(got[0].Included, ",") != "u1" {
		t.Fatalf("test segments = %+v", got)
	}
	if got := snap.Segments["production"]; len(got) != 1 || strings.Join(got[0].Included, ",") != "u2" {
		t.Fatalf("production segments = %+v (want the single GET's members)", got)
	}
	singles := 0
	for _, r := range f.requests {
		if strings.Count(strings.TrimPrefix(r.URL.Path, "/api/v2/segments/web/"), "/") == 1 {
			singles++
		}
	}
	if singles != 1 {
		t.Fatalf("%d single-segment GETs, want 1", singles)
	}
}

func TestRateLimitWaitsForTheReset(t *testing.T) {
	f := newFakeLD(t)
	limited := 0
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v2/projects/web/environments" || limited >= 2 {
			return false
		}
		limited++
		if limited == 1 {
			w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(time.Now().Add(1500*time.Millisecond).UnixMilli(), 10))
		} else {
			w.Header().Set("Retry-After", "2")
		}
		w.WriteHeader(429)
		return true
	}
	var sleeps []time.Duration
	if _, err := f.source(&sleeps).fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v", sleeps)
	}
	if sleeps[0] < 500*time.Millisecond || sleeps[0] > 2500*time.Millisecond {
		t.Fatalf("waited %s for a reset 1.5s away", sleeps[0])
	}
	if sleeps[1] != 2*time.Second {
		t.Fatalf("waited %s for Retry-After: 2", sleeps[1])
	}
}

func TestRateLimitGivesUpAfterRetries(t *testing.T) {
	f := newFakeLD(t)
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
		return true
	}
	_, err := f.source(nil).fetch(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIErrorsMapToExitCodes(t *testing.T) {
	for status, want := range map[int]int{401: exitcode.Auth, 403: exitcode.Auth, 404: exitcode.NotFound, 500: exitcode.Unexpected} {
		f := newFakeLD(t)
		f.before = func(w http.ResponseWriter, r *http.Request) bool {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"code":"x","message":"nope"}`)
			return true
		}
		_, err := f.source(nil).fetch(context.Background())
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.ExitCode() != want {
			t.Fatalf("%d: err = %v", status, err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("%d: the error shows the token: %v", status, err)
		}
		if status == 401 && !strings.Contains(err.Error(), "LAUNCHDARKLY_ACCESS_TOKEN") {
			t.Fatalf("401 error doesn't name the variable: %v", err)
		}
	}
}

func TestFetchFetchesSegmentsSinglyWhenTheListOmitsMembers(t *testing.T) {
	f := newFakeLD(t)
	f.segments["test"] = []map[string]any{{"key": "beta", "name": "Beta", "rules": []any{}}}
	f.single["test/beta"] = map[string]any{"key": "beta", "name": "Beta", "included": []string{"u9"}, "excluded": []string{}, "rules": []any{}}
	snap := fetchFrom(t, f)
	if got := snap.Segments["test"]; len(got) != 1 || strings.Join(got[0].Included, ",") != "u9" {
		t.Fatalf("test segments = %+v (want the single GET's members)", got)
	}
}
