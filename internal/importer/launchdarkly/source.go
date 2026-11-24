// Package launchdarkly reads a LaunchDarkly project through LaunchDarkly's
// REST API and translates its flags and segments for import.
package launchdarkly

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// Source reads one LaunchDarkly project. Token is an access token; one with
// the Reader role is enough, because the importer only reads.
type Source struct {
	BaseURL string
	Project string
	Token   string
	HTTP    *http.Client
	Sleep   func(time.Duration)
}

func (s *Source) Name() string { return "LaunchDarkly" }

func (s *Source) Load(ctx context.Context, opts im.Options) (*im.Result, error) {
	snap, err := s.fetch(ctx)
	if err != nil {
		return nil, err
	}
	return translate(snap, opts)
}

func (s *Source) fetch(ctx context.Context) (*snapshot, error) {
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	wait := func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	if s.Sleep != nil {
		wait = func(ctx context.Context, d time.Duration) error {
			s.Sleep(d)
			return ctx.Err()
		}
	}
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	c := &client{baseURL: base, token: s.Token, http: hc, wait: wait, now: time.Now}
	proj := url.PathEscape(s.Project)
	snap := &snapshot{Segments: map[string][]segment{}}

	var err error
	if snap.Environments, err = listAll[environment](ctx, c, "/api/v2/projects/"+proj+"/environments", nil); err != nil {
		return nil, err
	}

	// A flag list carries environment configuration only for the environment
	// it names, so it is read once per environment and merged by key.
	byKey := map[string]int{}
	for _, e := range snap.Environments {
		flags, err := listAll[flag](ctx, c, "/api/v2/flags/"+proj, url.Values{"env": {e.Key}, "summary": {"0"}})
		if err != nil {
			return nil, err
		}
		for _, f := range flags {
			cfg := f.Environments[e.Key]
			i, ok := byKey[f.Key]
			if !ok {
				i = len(snap.Flags)
				byKey[f.Key] = i
				f.Environments = map[string]*flagEnv{}
				snap.Flags = append(snap.Flags, f)
			}
			if cfg != nil {
				snap.Flags[i].Environments[e.Key] = cfg
			}
		}
	}

	archived, err := listAll[struct {
		Key string `json:"key"`
	}](ctx, c, "/api/v2/flags/"+proj, url.Values{"filter": {"state:archived"}, "summary": {"1"}})
	if err != nil {
		return nil, err
	}
	for _, a := range archived {
		snap.Archived = append(snap.Archived, a.Key)
	}

	for _, e := range snap.Environments {
		segPath := "/api/v2/segments/" + proj + "/" + url.PathEscape(e.Key)
		raws, err := listAll[json.RawMessage](ctx, c, segPath, nil)
		if err != nil {
			return nil, err
		}
		segs := []segment{}
		for _, raw := range raws {
			var probe map[string]json.RawMessage
			var sg segment
			if err := json.Unmarshal(raw, &probe); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &sg); err != nil {
				return nil, err
			}
			// A list entry that leaves out the rules or the member lists
			// is a summary: read the whole segment.
			full := true
			for _, field := range []string{"rules", "included", "excluded"} {
				if _, ok := probe[field]; !ok {
					full = false
				}
			}
			if !full {
				sg = segment{}
				if err := c.get(ctx, segPath+"/"+url.PathEscape(keyOf(probe)), &sg); err != nil {
					return nil, err
				}
			}
			segs = append(segs, sg)
		}
		snap.Segments[e.Key] = segs
	}
	return snap, nil
}

func keyOf(m map[string]json.RawMessage) string {
	var k string
	_ = json.Unmarshal(m["key"], &k)
	return k
}
