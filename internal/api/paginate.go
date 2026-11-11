package api

import (
	"context"
	"encoding/json"
	"net/url"
)

// Page is the public API's cursor-paginated list envelope.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

const pageSize = "100"

// ListAll fetches every page of a list route and returns the items in order.
// Use T = json.RawMessage to keep the API's own shape for --json output.
func ListAll[T any](ctx context.Context, c *Client, path Path, query url.Values) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = append([]string(nil), v...)
	}
	q.Set("limit", pageSize)

	var all []T
	for {
		var page Page[T]
		if err := c.Do(ctx, "GET", path, Request{Query: q}, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all, nil
		}
		q.Set("cursor", *page.NextCursor)
	}
}

// Decode converts raw items (as ListAll[json.RawMessage] returns them) into T.
func Decode[T any](raw []json.RawMessage) ([]T, error) {
	out := make([]T, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
