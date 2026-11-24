package launchdarkly

import (
	"context"
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

// DefaultBaseURL is LaunchDarkly's commercial instance. The EU instance is
// https://app.eu.launchdarkly.com and the federal one https://app.launchdarkly.us.
const DefaultBaseURL = "https://app.launchdarkly.com"

// apiVersion pins the REST API's response shapes.
const apiVersion = "20240415"

const pageSize = 50

// maxRateLimitRetries bounds how often one request waits out a 429.
const maxRateLimitRetries = 5

// maxWait caps one rate-limit wait. LaunchDarkly's windows are seconds long.
const maxWait = 60 * time.Second

type client struct {
	baseURL string
	token   string
	http    *http.Client
	// wait sleeps out a rate limit, returning early with the context's error
	// when it ends.
	wait func(context.Context, time.Duration) error
	now  func() time.Time
}

// APIError is a non-2xx answer from the LaunchDarkly API.
type APIError struct {
	Status  int
	Path    string
	Message string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return fmt.Sprintf("LaunchDarkly refused the access token (%d %s): check LAUNCHDARKLY_ACCESS_TOKEN, which needs at least the Reader role", e.Status, msg)
	}
	return fmt.Sprintf("LaunchDarkly API %s: %d %s", e.Path, e.Status, msg)
}

func (e *APIError) ExitCode() int {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return exitcode.Auth
	case http.StatusNotFound:
		return exitcode.NotFound
	}
	return exitcode.Unexpected
}

// get fetches one API path (with its query) and decodes the JSON body into out.
func (c *client) get(ctx context.Context, target string, out any) error {
	path, _, _ := strings.Cut(target, "?")
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+target, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.token)
		req.Header.Set("LD-API-Version", apiVersion)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", version.UserAgent())

		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRateLimitRetries {
			if err := c.wait(ctx, c.retryWait(resp.Header, attempt)); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(body, &e)
			return &APIError{Status: resp.StatusCode, Path: path, Message: e.Message}
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("LaunchDarkly API %s: unreadable response: %w", path, err)
		}
		return nil
	}
}

// retryWait reads how long a 429 asks to wait: X-Ratelimit-Reset is the
// epoch millisecond the window resets, Retry-After a number of seconds.
// Without either it backs off exponentially.
func (c *client) retryWait(h http.Header, attempt int) time.Duration {
	if ms, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-Ratelimit-Reset")), 10, 64); err == nil {
		d := time.UnixMilli(ms).Sub(c.now())
		return min(max(d, 0), maxWait)
	}
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxWait)
	}
	return time.Duration(1<<attempt) * time.Second
}

// page is one page of a LaunchDarkly list.
type page[T any] struct {
	Items      []T  `json:"items"`
	TotalCount *int `json:"totalCount"`
	Links      struct {
		Next *struct {
			Href string `json:"href"`
		} `json:"next"`
	} `json:"_links"`
}

// maxPages stops a list that never ends.
var maxPages = 10000

// listAll reads every page of a list by following its next links.
func listAll[T any](ctx context.Context, c *client, path string, query url.Values) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("limit", strconv.Itoa(pageSize))
	target := path + "?" + q.Encode()
	seen := map[string]bool{}
	var all []T
	for n := 0; n < maxPages; n++ {
		seen[pageKey(target)] = true
		var pg page[T]
		if err := c.get(ctx, target, &pg); err != nil {
			return nil, err
		}
		all = append(all, pg.Items...)
		if pg.Links.Next == nil || pg.Links.Next.Href == "" || len(pg.Items) == 0 ||
			(pg.TotalCount != nil && len(all) >= *pg.TotalCount) {
			return all, nil
		}
		next, err := url.Parse(pg.Links.Next.Href)
		if err != nil {
			return nil, fmt.Errorf("LaunchDarkly API %s: unreadable next link %q", path, pg.Links.Next.Href)
		}
		target = next.RequestURI()
		if seen[pageKey(target)] {
			return nil, fmt.Errorf("LaunchDarkly API %s: the next page link repeats %s", path, target)
		}
	}
	return nil, fmt.Errorf("LaunchDarkly API %s: still offering more after %d pages", path, maxPages)
}

// pageKey identifies a page whatever order its query parameters come in.
func pageKey(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	return u.Path + "?" + u.Query().Encode()
}
