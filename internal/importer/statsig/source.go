// Package statsig reads a Statsig project through the Statsig Console API and
// translates its feature gates, dynamic configs and segments for import.
package statsig

import (
	"context"
	"net/http"
	"strings"
	"time"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// Source reads one Statsig project. Key is a Console API key; a read-only one
// is enough, because the importer only reads.
type Source struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	Sleep   func(time.Duration)
}

func (s *Source) Name() string { return "Statsig" }

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
	// Unset, a rate-limit wait ends early when ctx is cancelled; Sleep is the
	// tests' seam.
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
	if sleep := s.Sleep; sleep != nil {
		wait = func(ctx context.Context, d time.Duration) error {
			sleep(d)
			return ctx.Err()
		}
	}
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	c := &client{baseURL: base, key: s.Key, http: hc, wait: wait}

	snap := &snapshot{Overrides: map[string]*overrides{}, IDLists: map[string][]string{}}
	var envList environmentsData
	if _, err := c.get(ctx, "/console/v1/environments", nil, &envList); err != nil {
		return nil, err
	}
	snap.Environments = envList.Environments

	var err error
	if snap.Gates, err = listAll[gate](ctx, c, "/console/v1/gates", nil); err != nil {
		return nil, err
	}
	asked, missing := 0, 0
	for _, g := range snap.Gates {
		if g.Status == "Archived" || g.IsTemplate {
			continue
		}
		o, found, err := c.gateOverrides(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		snap.Overrides[g.ID] = o
		asked++
		if !found {
			missing++
		}
	}
	// One gate without overrides may answer 404; every gate answering it
	// looks like the endpoint itself is missing.
	snap.OverridesUnread = asked > 1 && missing == asked
	if snap.Configs, err = listAll[dynamicConfig](ctx, c, "/console/v1/dynamic_configs", nil); err != nil {
		return nil, err
	}
	if snap.Segments, err = listAll[segment](ctx, c, "/console/v1/segments", nil); err != nil {
		return nil, err
	}
	for _, sg := range snap.Segments {
		if sg.Type != "id_list" {
			continue
		}
		if snap.IDLists[sg.ID], err = c.idListMembers(ctx, sg.ID); err != nil {
			return nil, err
		}
	}
	return snap, nil
}
