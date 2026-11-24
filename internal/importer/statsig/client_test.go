package statsig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// fakeConsole serves a snapshot the way the Console API does: list endpoints
// one item per page (to exercise paging), everything wrapped in {data}.
type fakeConsole struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	// before, when set, answers instead of the snapshot for matching requests.
	before func(w http.ResponseWriter, r *http.Request) bool
}

func newFakeConsole(t *testing.T, snap *snapshot) *fakeConsole {
	t.Helper()
	f := &fakeConsole{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r)
		before := f.before
		f.mu.Unlock()
		if before != nil && before(w, r) {
			return
		}
		serveSnapshot(t, snap, w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func serveSnapshot(t *testing.T, snap *snapshot, w http.ResponseWriter, r *http.Request) {
	page := 1
	fmt.Sscan(r.URL.Query().Get("page"), &page)
	writeData := func(data any, pg map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{"message": "ok", "data": data}
		if pg != nil {
			body["pagination"] = pg
		}
		json.NewEncoder(w).Encode(body)
	}
	list := func(n int, item func(i int) any) {
		var next any
		if page < n {
			next = fmt.Sprintf("%s?page=%d", r.URL.Path, page+1)
		}
		items := []any{}
		if page >= 1 && page <= n {
			items = append(items, item(page-1))
		}
		writeData(items, map[string]any{"itemsPerPage": 1, "pageNumber": page, "totalItems": n, "nextPage": next})
	}
	path := r.URL.Path
	switch {
	case path == "/console/v1/environments":
		writeData(map[string]any{"environments": snap.Environments}, nil)
	case path == "/console/v1/gates":
		list(len(snap.Gates), func(i int) any { return snap.Gates[i] })
	case path == "/console/v1/dynamic_configs":
		list(len(snap.Configs), func(i int) any { return snap.Configs[i] })
	case path == "/console/v1/segments":
		list(len(snap.Segments), func(i int) any { return snap.Segments[i] })
	case strings.HasPrefix(path, "/console/v1/gates/") && strings.HasSuffix(path, "/overrides"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/console/v1/gates/"), "/overrides")
		o, ok := snap.Overrides[id]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"status":404,"message":"no overrides"}`)
			return
		}
		writeData(o, nil)
	case strings.HasPrefix(path, "/console/v1/segments/") && strings.HasSuffix(path, "/id_list"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/console/v1/segments/"), "/id_list")
		ids := snap.IDLists[id]
		// One id per page.
		var next any
		if page < len(ids) {
			next = fmt.Sprintf("%s?page=%d", path, page+1)
		}
		chunk := []string{}
		if page >= 1 && page <= len(ids) {
			chunk = ids[page-1 : page]
		}
		writeData(map[string]any{"name": id, "count": len(ids), "ids": chunk},
			map[string]any{"pageNumber": page, "totalItems": len(ids), "nextPage": next})
	default:
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"status":404,"message":"no route %s"}`, path)
	}
}

func TestLoadReadsEveryPageAndTranslatesLikeTheSnapshot(t *testing.T) {
	snap := loadSnapshot(t)
	f := newFakeConsole(t, snap)
	src := &Source{BaseURL: f.srv.URL, Key: "console-secret", Sleep: func(time.Duration) {}}
	got, err := src.Load(context.Background(), im.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := translate(snap, im.Options{})
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got.Document())
	wantJSON, _ := json.Marshal(want.Document())
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("reading through the API differs from the snapshot:\n got %s\nwant %s", gotJSON, wantJSON)
	}

	for _, r := range f.requests {
		if r.Header.Get("STATSIG-API-KEY") != "console-secret" || r.Header.Get("STATSIG-API-VERSION") != "20240601" {
			t.Fatalf("%s sent key %q version %q", r.URL.Path, r.Header.Get("STATSIG-API-KEY"), r.Header.Get("STATSIG-API-VERSION"))
		}
		if r.Method != http.MethodGet {
			t.Fatalf("the importer only reads; it sent %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestRateLimitIsWaitedOut(t *testing.T) {
	snap := loadSnapshot(t)
	f := newFakeConsole(t, snap)
	limited := 0
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/console/v1/environments" && limited < 2 {
			limited++
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	}
	var slept []time.Duration
	src := &Source{BaseURL: f.srv.URL, Key: "k", Sleep: func(d time.Duration) { slept = append(slept, d) }}
	if _, err := src.Load(context.Background(), im.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 || slept[0] != 3*time.Second {
		t.Fatalf("slept %v, want two 3s waits", slept)
	}
}

func TestRefusedKeyIsAnAuthError(t *testing.T) {
	f := newFakeConsole(t, loadSnapshot(t))
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"status":401,"message":"Invalid API key"}`)
		return true
	}
	src := &Source{BaseURL: f.srv.URL, Key: "bad", Sleep: func(time.Duration) {}}
	_, err := src.Load(context.Background(), im.Options{})
	if err == nil || exitcode.Classify(err) != exitcode.Auth {
		t.Fatalf("err = %v (exit %d), want an auth error", err, exitcode.Classify(err))
	}
	if strings.Contains(err.Error(), "bad") {
		t.Fatalf("the error must not echo the key: %v", err)
	}
	if !strings.Contains(err.Error(), "STATSIG_CONSOLE_API_KEY") {
		t.Fatalf("the error should say which key to check: %v", err)
	}
}

func TestServerErrorStopsTheRead(t *testing.T) {
	f := newFakeConsole(t, loadSnapshot(t))
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/console/v1/dynamic_configs" {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return false
	}
	src := &Source{BaseURL: f.srv.URL, Key: "k", Sleep: func(time.Duration) {}}
	if _, err := src.Load(context.Background(), im.Options{}); err == nil || !strings.Contains(err.Error(), "/console/v1/dynamic_configs: 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestShortIDListIsAnError(t *testing.T) {
	f := newFakeConsole(t, loadSnapshot(t))
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/console/v1/segments/vip/id_list" {
			// Two of five IDs and no pagination block: the rest would be lost.
			fmt.Fprint(w, `{"message":"ok","data":{"name":"vip","count":5,"ids":["u1","u2"]}}`)
			return true
		}
		return false
	}
	src := &Source{BaseURL: f.srv.URL, Key: "k", Sleep: func(time.Duration) {}}
	_, err := src.Load(context.Background(), im.Options{})
	if err == nil || !strings.Contains(err.Error(), "read 2 of the segment's 5 IDs") {
		t.Fatalf("err = %v", err)
	}
}

func TestEndlessPagingStops(t *testing.T) {
	defer func(n int) { maxPages = n }(maxPages)
	maxPages = 5
	f := newFakeConsole(t, loadSnapshot(t))
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/console/v1/gates" {
			// Ignores the page number and always offers another page.
			fmt.Fprint(w, `{"message":"ok","data":[{"id":"loop","idType":"userID","isEnabled":true,"rules":[]}],"pagination":{"nextPage":"/console/v1/gates?page=2"}}`)
			return true
		}
		return false
	}
	src := &Source{BaseURL: f.srv.URL, Key: "k", Sleep: func(time.Duration) {}}
	if _, err := src.Load(context.Background(), im.Options{}); err == nil || !strings.Contains(err.Error(), "still offering more") {
		t.Fatalf("err = %v", err)
	}
}

func TestOverridesThatNeverAnswerAreReported(t *testing.T) {
	f := newFakeConsole(t, loadSnapshot(t))
	f.before = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/overrides") {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return false
	}
	src := &Source{BaseURL: f.srv.URL, Key: "k", Sleep: func(time.Duration) {}}
	res, err := src.Load(context.Background(), im.Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustIssue(t, res, "", "overrides-unread", im.Warning)
}
