package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a client at handler and records every backoff sleep.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	var sleeps []time.Duration
	c := New(Options{
		BaseURL: srv.URL,
		Token:   "ffp_test_token",
		Sleep:   func(d time.Duration) { sleeps = append(sleeps, d) },
	})
	return c, &sleeps
}

func statusSequence(calls *int32, statuses ...int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		i := atomic.AddInt32(calls, 1) - 1
		st := statuses[len(statuses)-1]
		if int(i) < len(statuses) {
			st = statuses[i]
		}
		if st == http.StatusNoContent {
			w.WriteHeader(st)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		if st < 300 {
			w.Write([]byte(`{}`))
		} else {
			w.Write([]byte(`{"error":"x","message":"failed","docs_url":"d"}`))
		}
	}
}

func TestSendsAuthAcceptAndUserAgent(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{}`))
	})
	if err := c.Do(context.Background(), "GET", P("me"), Request{}, nil); err != nil {
		t.Fatal(err)
	}
	if v := got.Get("Authorization"); v != "Bearer ffp_test_token" {
		t.Errorf("Authorization = %q", v)
	}
	if v := got.Get("Accept"); v != "application/json" {
		t.Errorf("Accept = %q", v)
	}
	if v := got.Get("User-Agent"); v != "featureflip-cli/dev" {
		t.Errorf("User-Agent = %q", v)
	}
}

func TestEscapesPathSegments(t *testing.T) {
	var raw string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw = r.RequestURI
		w.Write([]byte(`{}`))
	})
	err := c.Do(context.Background(), "GET", P("orgs", "acme", "projects", "p", "flags", "a b%2F"), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/api/v1/orgs/acme/projects/p/flags/a%20b%252F"; raw != want {
		t.Errorf("path = %q, want %q", raw, want)
	}
}

func TestReasonIsPercentEncoded(t *testing.T) {
	var got string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Intent-Reason")
		w.WriteHeader(204)
	})
	if err := c.Do(context.Background(), "POST", P("x"), Request{Reason: "déploy 🚀\nnow"}, nil); err != nil {
		t.Fatal(err)
	}
	if want := "d%C3%A9ploy%20%F0%9F%9A%80%0Anow"; got != want {
		t.Errorf("X-Intent-Reason = %q, want %q", got, want)
	}
}

func TestGetRetries502Then200(t *testing.T) {
	var calls int32
	c, sleeps := newTestClient(t, statusSequence(&calls, 502, 200))
	if err := c.Do(context.Background(), "GET", P("orgs"), Request{}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if want := []time.Duration{250 * time.Millisecond}; !reflect.DeepEqual(*sleeps, want) {
		t.Errorf("sleeps = %v, want %v", *sleeps, want)
	}
}

func TestGetGivesUpAfterThreeAttempts(t *testing.T) {
	var calls int32
	c, sleeps := newTestClient(t, statusSequence(&calls, 503))
	err := c.Do(context.Background(), "GET", P("orgs"), Request{}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Fatalf("err = %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if want := []time.Duration{250 * time.Millisecond, time.Second}; !reflect.DeepEqual(*sleeps, want) {
		t.Errorf("sleeps = %v, want %v", *sleeps, want)
	}
}

func TestUnkeyedPostRetriesOnly503(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, statusSequence(&calls, 502, 200))
	if err := c.Do(context.Background(), "POST", P("x"), Request{}, nil); err == nil {
		t.Fatal("expected the 502 to surface")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}

	calls = 0
	c, _ = newTestClient(t, statusSequence(&calls, 503, 200))
	if err := c.Do(context.Background(), "POST", P("x"), Request{}, nil); err != nil {
		t.Fatalf("503 should be retried: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestKeyedPostRetries502(t *testing.T) {
	var calls int32
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		statusSequence(&calls, 502, 200)(w, r)
	})
	if err := c.Do(context.Background(), "POST", P("x"), Request{IdempotencyKey: "k-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"k-1", "k-1"}) {
		t.Errorf("keys = %v", keys)
	}
}

func TestReplayablePostRetries502(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, statusSequence(&calls, 502, 204))
	if err := c.Do(context.Background(), "POST", P("x"), Request{Replayable: true}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestRetryAfterHeaderRetriesOnce(t *testing.T) {
	var calls int32
	c, sleeps := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`{}`))
	})
	if err := c.Do(context.Background(), "POST", P("x"), Request{}, nil); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{2 * time.Second}; !reflect.DeepEqual(*sleeps, want) {
		t.Errorf("sleeps = %v, want %v", *sleeps, want)
	}
}

func TestRetryAfterOver10sFails(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
	})
	err := c.Do(context.Background(), "GET", P("x"), Request{}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if !strings.Contains(err.Error(), "30s") {
		t.Errorf("message %q should mention 30s", err.Error())
	}
}

func TestParsesErrorEnvelope(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"flag_not_found","message":"No flag 'new-chekout'.","docs_url":"https://d","did_you_mean":["new-checkout"],"fields":{"flag":["bad"]}}`))
	})
	err := c.Do(context.Background(), "GET", P("x"), Request{}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if apiErr.Code != "flag_not_found" || apiErr.Message != "No flag 'new-chekout'." || apiErr.DocsURL != "https://d" {
		t.Errorf("envelope = %+v", apiErr)
	}
	if !reflect.DeepEqual(apiErr.DidYouMean, []string{"new-checkout"}) || !reflect.DeepEqual(apiErr.Fields["flag"], []string{"bad"}) {
		t.Errorf("envelope = %+v", apiErr)
	}
	if apiErr.ExitCode() != 4 {
		t.Errorf("exit = %d, want 4", apiErr.ExitCode())
	}
}

func TestNonJSONErrorBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(502)
		w.Write([]byte(`<html><body>Bad Gateway</body></html>`))
	})
	err := c.Do(context.Background(), "GET", P("orgs"), Request{Query: map[string][]string{"limit": {"100"}}}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if apiErr.Code != "http_502" || apiErr.Message != "HTTP 502 from /api/v1/orgs" {
		t.Errorf("got code %q message %q", apiErr.Code, apiErr.Message)
	}
}

func TestPlanLimitIsRejectedNotAuth(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"plan_limit_reached","message":"Solo allows 10 flags.","docs_url":"d","plan_limit":{"limit":10}}`))
	})
	err := c.Do(context.Background(), "POST", P("x"), Request{}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.ExitCode() != 5 {
		t.Fatalf("err = %v", err)
	}
}

func TestExitCodesByStatus(t *testing.T) {
	cases := map[int]int{400: 5, 401: 3, 403: 3, 404: 4, 409: 5, 412: 5, 422: 5, 500: 1}
	for status, want := range cases {
		e := &Error{Status: status, Code: "x"}
		if got := e.ExitCode(); got != want {
			t.Errorf("status %d: exit %d, want %d", status, got, want)
		}
	}
	if got := (&Error{Status: 400, Code: "change_reason_required"}).ExitCode(); got != 2 {
		t.Errorf("change_reason_required: exit %d, want 2", got)
	}
}

func TestTokenNeverInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, Token: "ffp_secret123", Sleep: func(time.Duration) {}})
	err := c.Do(context.Background(), "GET", P("me"), Request{}, nil)
	if err == nil || strings.Contains(err.Error(), "ffp_secret123") {
		t.Fatalf("err = %v", err)
	}

	// A connection failure's error text must not leak it either.
	c = New(Options{BaseURL: "http://127.0.0.1:1", Token: "ffp_secret123", Sleep: func(time.Duration) {}})
	err = c.Do(context.Background(), "GET", P("me"), Request{}, nil)
	if err == nil || strings.Contains(err.Error(), "ffp_secret123") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewIdempotencyKeyIsUUIDv4(t *testing.T) {
	k := NewIdempotencyKey()
	if len(k) != 36 || k[14] != '4' || strings.Count(k, "-") != 4 {
		t.Errorf("key %q is not a v4 UUID", k)
	}
	if NewIdempotencyKey() == k {
		t.Error("keys repeat")
	}
}
