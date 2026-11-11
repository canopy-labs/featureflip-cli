// Package api is a small client for the Featureflip public API (/api/v1),
// covering only the routes the CLI calls.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
	"github.com/canopy-labs/featureflip-cli/internal/version"
)

// Gateway statuses a rolling deploy produces while pods drain. A 503 is what
// the gateway answers when it cannot reach a pod at all, so the request never
// ran and is safe to send again whatever its method.
var (
	backoffs          = []time.Duration{250 * time.Millisecond, time.Second}
	transientStatuses = map[int]bool{502: true, 503: true, 504: true}
)

// maxRetryAfter is the longest rate-limit wait the CLI sits through by itself.
const maxRetryAfter = 10 * time.Second

type Options struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Sleep      func(time.Duration)
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	sleep   func(time.Duration)
}

func New(o Options) *Client {
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	sleep := o.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	return &Client{
		baseURL: strings.TrimRight(o.BaseURL, "/"),
		token:   o.Token,
		http:    hc,
		sleep:   sleep,
	}
}

// Path is a route under /api/v1, one element per segment. Segments are
// escaped individually, so a key containing "/" or "%" stays one segment.
type Path []string

func P(segments ...string) Path { return Path(segments) }

func (p Path) String() string {
	escaped := make([]string, len(p))
	for i, s := range p {
		escaped[i] = url.PathEscape(s)
	}
	return "/api/v1/" + strings.Join(escaped, "/")
}

type Request struct {
	Query          url.Values
	Body           any
	IdempotencyKey string
	// Reason is sent as X-Intent-Reason, the change reason environments can require.
	Reason string
	// Replayable marks a POST that is safe to send twice (it sets state rather
	// than creating something), so it retries like a GET.
	Replayable bool
}

// Error is a non-2xx response, decoded from the API's error envelope when
// there is one.
type Error struct {
	Status      int
	Code        string
	Message     string
	DocsURL     string
	Fields      map[string][]string
	DidYouMean  []string
	RetryAfter  *int
	PlanLimit   json.RawMessage
	NextActions json.RawMessage
	// Wait is how long the server asked us to wait before retrying (429).
	Wait time.Duration
}

func (e *Error) Error() string {
	msg := e.Message
	if e.Status == http.StatusTooManyRequests && e.Wait > 0 {
		if msg == "" {
			msg = "rate limited by the Featureflip API"
		}
		msg = fmt.Sprintf("%s (retry after %s)", msg, e.Wait)
	}
	return msg
}

// ExitCode maps the error onto the CLI's exit codes. The envelope's error
// code is checked first: a plan limit arrives as a 403 but is a refusal, not
// an authentication problem.
func (e *Error) ExitCode() int {
	switch e.Code {
	case "plan_limit_reached":
		return exitcode.Rejected
	case "change_reason_required":
		return exitcode.Usage
	}
	switch e.Status {
	case 401, 403:
		return exitcode.Auth
	case 404:
		return exitcode.NotFound
	case 400, 409, 412, 422:
		return exitcode.Rejected
	}
	return exitcode.Unexpected
}

type envelope struct {
	Error       string              `json:"error"`
	Message     string              `json:"message"`
	DocsURL     string              `json:"docs_url"`
	Fields      map[string][]string `json:"fields"`
	RetryAfter  *int                `json:"retry_after"`
	DidYouMean  []string            `json:"did_you_mean"`
	PlanLimit   json.RawMessage     `json:"plan_limit"`
	NextActions json.RawMessage     `json:"next_actions"`
}

type noTokenError struct{}

func (noTokenError) Error() string {
	return `no token: set FEATUREFLIP_TOKEN or run "featureflip login"`
}
func (noTokenError) ExitCode() int { return exitcode.Auth }

// ErrNoToken is returned when no token is configured anywhere.
var ErrNoToken error = noTokenError{}

// Do sends one API request, retrying where that is safe, and decodes a 2xx
// JSON body into out (if out is non-nil and the response has a body).
func (c *Client) Do(ctx context.Context, method string, path Path, req Request, out any) error {
	target := c.baseURL + path.String()
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}

	var body []byte
	if req.Body != nil {
		var err error
		if body, err = json.Marshal(req.Body); err != nil {
			return err
		}
	}

	replayable := req.Replayable || req.IdempotencyKey != "" ||
		method == http.MethodGet || method == http.MethodHead ||
		method == http.MethodPut || method == http.MethodDelete

	rateLimitRetried := false
	for attempt := 0; ; attempt++ {
		retriesLeft := attempt < len(backoffs)
		resp, err := c.send(ctx, method, target, body, req)
		if err != nil {
			if ctx.Err() != nil || !replayable || !retriesLeft {
				return err
			}
			c.sleep(backoffs[attempt])
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			apiErr := decodeError(resp, path)
			if !rateLimitRetried && apiErr.Wait > 0 && apiErr.Wait <= maxRetryAfter {
				rateLimitRetried = true
				c.sleep(apiErr.Wait)
				attempt-- // a rate-limit wait doesn't spend the 5xx retry budget
				continue
			}
			return apiErr
		}

		retryable := transientStatuses[resp.StatusCode]
		if !replayable {
			retryable = resp.StatusCode == http.StatusServiceUnavailable
		}
		if retryable && retriesLeft {
			drain(resp)
			c.sleep(backoffs[attempt])
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return decodeError(resp, path)
		}
		defer resp.Body.Close()
		if out == nil || resp.StatusCode == http.StatusNoContent {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func (c *Client) send(ctx context.Context, method, target string, body []byte, req Request) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	r, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", version.UserAgent())
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if req.IdempotencyKey != "" {
		r.Header.Set("Idempotency-Key", req.IdempotencyKey)
	}
	if strings.TrimSpace(req.Reason) != "" {
		r.Header.Set("X-Intent-Reason", encodeURIComponent(req.Reason))
	}
	return c.http.Do(r)
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

func decodeError(resp *http.Response, path Path) *Error {
	defer drain(resp)
	e := &Error{Status: resp.StatusCode}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env envelope
	if json.Unmarshal(raw, &env) == nil && env.Error != "" {
		e.Code, e.Message, e.DocsURL = env.Error, env.Message, env.DocsURL
		e.Fields, e.DidYouMean, e.RetryAfter = env.Fields, env.DidYouMean, env.RetryAfter
		e.PlanLimit, e.NextActions = env.PlanLimit, env.NextActions
	} else {
		e.Code = fmt.Sprintf("http_%d", resp.StatusCode)
		e.Message = fmt.Sprintf("HTTP %d from %s", resp.StatusCode, path.String())
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s >= 0 {
			e.Wait = time.Duration(s) * time.Second
		} else if e.RetryAfter != nil {
			e.Wait = time.Duration(*e.RetryAfter) * time.Second
		}
	}
	return e
}

// encodeURIComponent matches JavaScript's function of the same name, which is
// how the server expects the change reason to be encoded.
func encodeURIComponent(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if strings.IndexByte(unreserved, ch) >= 0 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// NewIdempotencyKey returns a random version-4 UUID.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
