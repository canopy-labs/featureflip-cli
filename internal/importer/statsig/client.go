package statsig

import (
	"context"
	"encoding/json"
	"errors"
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

// DefaultBaseURL is the Statsig Console API.
const DefaultBaseURL = "https://statsigapi.net"

// apiVersion is the only Console API version Statsig publishes.
const apiVersion = "20240601"

const pageSize = 100

// maxRateLimitRetries bounds how often one request waits out a 429.
const maxRateLimitRetries = 5

type client struct {
	baseURL string
	key     string
	http    *http.Client
	wait    func(context.Context, time.Duration) error
}

// APIError is a non-2xx answer from the Console API.
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
		return fmt.Sprintf("Statsig refused the Console API key (%d %s): check STATSIG_CONSOLE_API_KEY", e.Status, msg)
	}
	return fmt.Sprintf("Statsig Console API %s: %d %s", e.Path, e.Status, msg)
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

// get fetches one Console API path and decodes the response's data field.
func (c *client) get(ctx context.Context, path string, query url.Values, data any) (*pagination, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("STATSIG-API-KEY", c.key)
		req.Header.Set("STATSIG-API-VERSION", apiVersion)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", version.UserAgent())

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRateLimitRetries {
			if err := c.wait(ctx, retryAfter(resp.Header.Get("Retry-After"), attempt)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(body, &e)
			return nil, &APIError{Status: resp.StatusCode, Path: path, Message: e.Message}
		}

		var env struct {
			Data       json.RawMessage `json:"data"`
			Pagination *pagination     `json:"pagination"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, fmt.Errorf("Statsig Console API %s: unreadable response: %w", path, err)
		}
		if data != nil && len(env.Data) > 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, data); err != nil {
				return nil, fmt.Errorf("Statsig Console API %s: unreadable data: %w", path, err)
			}
		}
		return env.Pagination, nil
	}
}

type pagination struct {
	ItemsPerPage int     `json:"itemsPerPage"`
	PageNumber   int     `json:"pageNumber"`
	TotalItems   int     `json:"totalItems"`
	NextPage     *string `json:"nextPage"`
}

// maxPages stops a list that never ends, e.g. a server that ignores the page
// number and always offers a next page.
var maxPages = 10000

// listAll fetches every page of a list endpoint.
func listAll[T any](ctx context.Context, c *client, path string, query url.Values) ([]T, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		var items []T
		pg, err := c.get(ctx, path, q, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if pg == nil || pg.NextPage == nil || *pg.NextPage == "" || len(items) == 0 ||
			(pg.TotalItems > 0 && len(all) >= pg.TotalItems) {
			return all, nil
		}
	}
	return nil, fmt.Errorf("Statsig Console API %s: still offering more after %d pages", path, maxPages)
}

// idListMembers fetches every member of an ID-list segment, and refuses a
// list that came back shorter than the count Statsig reports for it.
func (c *client) idListMembers(ctx context.Context, id string) ([]string, error) {
	var all []string
	path := "/console/v1/segments/" + url.PathEscape(id) + "/id_list"
	expected := -1
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"limit": {strconv.Itoa(1000)}, "page": {strconv.Itoa(page)}}
		var list idList
		pg, err := c.get(ctx, path, q, &list)
		if err != nil {
			return nil, err
		}
		if expected < 0 {
			expected = list.Count
		}
		all = append(all, list.IDs...)
		if pg == nil || pg.NextPage == nil || *pg.NextPage == "" || len(list.IDs) == 0 || len(all) >= expected {
			if len(all) < expected {
				return nil, fmt.Errorf("Statsig Console API %s: read %d of the segment's %d IDs", path, len(all), expected)
			}
			return all, nil
		}
	}
	return nil, fmt.Errorf("Statsig Console API %s: still offering more after %d pages", path, maxPages)
}

// gateOverrides fetches a gate's ID overrides. found is false when Statsig
// answered 404, which it may do for a gate with none.
func (c *client) gateOverrides(ctx context.Context, id string) (o *overrides, found bool, err error) {
	o = &overrides{}
	_, err = c.get(ctx, "/console/v1/gates/"+url.PathEscape(id)+"/overrides", nil, o)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return &overrides{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return o, true, nil
}

// retryAfter reads a Retry-After header in seconds, or backs off exponentially.
func retryAfter(header string, attempt int) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && s >= 0 && s <= 60 {
		return time.Duration(s) * time.Second
	}
	return time.Duration(1<<attempt) * time.Second
}
