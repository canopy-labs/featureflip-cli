package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTerminal scripts the interactive side of a run: whether it counts as a
// terminal, and the answers handed back to successive prompts.
type fakeTerminal struct {
	interactive bool
	answers     []string
	prompts     []string
}

func (f *fakeTerminal) Interactive() bool { return f.interactive }

func (f *fakeTerminal) Prompt(msg string) (string, error) {
	f.prompts = append(f.prompts, msg)
	if len(f.answers) == 0 {
		return "", io.EOF
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

func (f *fakeTerminal) PromptSecret(msg string) (string, error) { return f.Prompt(msg) }

// recorded is one request the fake API received.
type recorded struct {
	Method, Path string // Path is the escaped path, without the query
	Query        url.Values
	Header       http.Header
	Body         string
}

// fakeAPI is an in-process stand-in for the public API. Routes are matched on
// method and escaped path; anything unrouted is a 404 envelope.
type fakeAPI struct {
	srv      *httptest.Server
	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	requests []recorded
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recorded{r.Method, r.URL.EscapedPath(), r.URL.Query(), r.Header.Clone(), string(body)})
		h := f.routes[r.Method+" "+r.URL.EscapedPath()]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error":"not_found","message":"no fake route for %s %s","docs_url":"d"}`, r.Method, r.URL.EscapedPath())
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// on serves a fixed status and JSON body. A 204 ignores body.
func (f *fakeAPI) on(method, path string, status int, body string) {
	f.handle(method, path, func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func (f *fakeAPI) handle(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = h
}

func (f *fakeAPI) calls(method, path string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.requests {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeAPI) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.Method == method {
			n++
		}
	}
	return n
}

const testToken = "ffp_test_token"

type runOpts struct {
	Env       map[string]string
	Stdin     string
	Terminal  *fakeTerminal
	ConfigDir string
	// ConfigDirErr makes Env.ConfigDir fail, as os.UserConfigDir does with
	// neither $XDG_CONFIG_HOME nor $HOME set.
	ConfigDirErr error
	Sleeps       *[]time.Duration
	API          *fakeAPI
	NoToken      bool // don't default FEATUREFLIP_TOKEN
	StdoutTTY    bool // stdout is a terminal (enables color)
}

// run executes the CLI in-process with everything injected and returns what
// it wrote and the exit code it chose.
func run(t *testing.T, opts runOpts, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	env, out, errOut := newTestEnv(t, opts)
	code = Execute(context.Background(), args, env)
	return out.String(), errOut.String(), code
}

// newTestEnv builds the Env run uses, for tests that call internals directly.
func newTestEnv(t *testing.T, opts runOpts) (Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	if opts.ConfigDir == "" {
		opts.ConfigDir = t.TempDir()
	}
	if opts.Terminal == nil {
		opts.Terminal = &fakeTerminal{}
	}
	vars := map[string]string{}
	for k, v := range opts.Env {
		vars[k] = v
	}
	if opts.API != nil {
		if _, ok := vars["FEATUREFLIP_API_URL"]; !ok {
			vars["FEATUREFLIP_API_URL"] = opts.API.srv.URL
		}
	}
	if _, ok := vars["FEATUREFLIP_TOKEN"]; !ok && !opts.NoToken {
		vars["FEATUREFLIP_TOKEN"] = testToken
	}
	opts.Env = vars
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	env := Env{
		In:        strings.NewReader(opts.Stdin),
		Out:       out,
		Err:       errOut,
		Getenv:    func(k string) string { return opts.Env[k] },
		Terminal:  opts.Terminal,
		StdoutTTY: opts.StdoutTTY,
		ConfigDir: func() (string, error) {
			if opts.ConfigDirErr != nil {
				return "", opts.ConfigDirErr
			}
			return opts.ConfigDir, nil
		},
		Sleep: func(d time.Duration) {
			if opts.Sleeps != nil {
				*opts.Sleeps = append(*opts.Sleeps, d)
			}
		},
	}
	return env, out, errOut
}
