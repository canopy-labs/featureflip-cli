package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canopy-labs/featureflip-cli/internal/api"
)

// fakeTarget is a stateful stand-in for one Featureflip project behind the
// public API, holding what the importer writes and serving it back the way
// the API does, so a second import can be checked against the first.
type fakeTarget struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	plan     string
	flagCap  *int
	noLimits bool
	envs     []string
	confirm  map[string]bool // environments that require confirmation
	flags    map[string]*fakeFlag
	segments map[string]*api.Segment
	nextID   int
	// writes records every non-GET request as "METHOD path".
	writes []string
	// requests records every request with its headers.
	requests []*http.Request
	// failOn makes the first request whose "METHOD path" has this suffix fail
	// with a 500.
	failOn string
	// frozen answers every write with a 423 change_frozen, unless the request
	// carries X-Freeze-Override: true.
	frozen bool
	// idem maps an Idempotency-Key to the response body it first produced.
	idem map[string][]byte
}

type fakeFlag struct {
	ID, Key, Type string
	Archived      bool
	ClientSide    bool
	Variations    []api.Variation
	Envs          map[string]*fakeEnv
}

type fakeEnv struct {
	Enabled  bool
	Default  string // variation id
	Strategy string
	Rules    []api.TargetingRule
	Prereqs  []api.Prerequisite
}

const testOrg, testProject = "acme", "web"

func newFakeTarget(t *testing.T, envs ...string) *fakeTarget {
	t.Helper()
	f := &fakeTarget{
		t: t, plan: "Team", envs: envs, confirm: map[string]bool{},
		flags: map[string]*fakeFlag{}, segments: map[string]*api.Segment{}, idem: map[string][]byte{},
	}
	mux := http.NewServeMux()
	base := "/api/v1/orgs/" + testOrg + "/projects/" + testProject
	mux.HandleFunc("GET /api/v1/orgs/"+testOrg, f.getOrg)
	mux.HandleFunc("GET "+base+"/environments", f.listEnvs)
	mux.HandleFunc("GET "+base+"/flags", f.listFlags)
	mux.HandleFunc("POST "+base+"/flags", f.createFlag)
	mux.HandleFunc("GET "+base+"/flags/{flag}", f.getFlag)
	mux.HandleFunc("GET "+base+"/flags/{flag}/environments", f.listFlagEnvs)
	mux.HandleFunc("PUT "+base+"/flags/{flag}/environments/{env}", f.putFlagEnv)
	mux.HandleFunc("GET "+base+"/flags/{flag}/environments/{env}/targeting", f.getTargeting)
	mux.HandleFunc("PUT "+base+"/flags/{flag}/environments/{env}/targeting", f.putTargeting)
	mux.HandleFunc("POST "+base+"/flags/{flag}/environments/{env}/toggle", f.toggle)
	mux.HandleFunc("GET "+base+"/segments", f.listSegments)
	mux.HandleFunc("POST "+base+"/segments", f.createSegment)
	mux.HandleFunc("PUT "+base+"/segments/{segment}", f.putSegment)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(r.Context()))
		call := r.Method + " " + r.URL.Path
		if r.Method != http.MethodGet {
			f.writes = append(f.writes, call)
		}
		fail := f.failOn != "" && strings.HasSuffix(call, f.failOn)
		if fail {
			f.failOn = ""
		}
		frozen := f.frozen && r.Method != http.MethodGet && r.Header.Get("X-Freeze-Override") != "true"
		f.mu.Unlock()
		if frozen {
			w.WriteHeader(423)
			io.WriteString(w, `{"error":"change_frozen","message":"frozen","docs_url":"d","change_freeze":{"id":"f1","name":"Holiday freeze","environment_key":"prod","ends_at":null,"can_override":true}}`)
			return
		}
		if fail {
			w.WriteHeader(500)
			io.WriteString(w, `{"error":"internal_error","message":"boom","docs_url":"d"}`)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTarget) client() *api.Client {
	return api.New(api.Options{BaseURL: f.srv.URL, Token: "t", Sleep: func(time.Duration) {}})
}

func (f *fakeTarget) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%04d", prefix, f.nextID)
}

func (f *fakeTarget) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(404)
	io.WriteString(w, `{"error":"not_found","message":"not found","docs_url":"d"}`)
}

func (f *fakeTarget) getOrg(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	org := api.Org{ID: "org-1", Name: "Acme", Slug: testOrg, Plan: f.plan}
	if !f.noLimits {
		org.Limits = &api.PlanLimits{FlagsPerProject: f.flagCap}
	}
	f.writeJSON(w, 200, org)
}

func (f *fakeTarget) listEnvs(w http.ResponseWriter, r *http.Request) {
	var items []api.Environment
	for i, e := range f.envs {
		items = append(items, api.Environment{ID: "env-" + e, Key: e, Name: e, SortOrder: i, RequireConfirmation: f.confirm[e]})
	}
	f.writeJSON(w, 200, api.Page[api.Environment]{Items: items})
}

func (f *fakeTarget) listFlags(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := []api.FlagListItem{}
	for _, k := range sortedKeys(f.flags) {
		fl := f.flags[k]
		items = append(items, api.FlagListItem{ID: fl.ID, Key: fl.Key, Name: fl.Key, Type: fl.Type, IsArchived: fl.Archived})
	}
	f.writeJSON(w, 200, api.Page[api.FlagListItem]{Items: items})
}

// addFlag puts a flag straight into the project, off with no rules.
func (f *fakeTarget) addFlag(key, typ string, variations ...api.Variation) *fakeFlag {
	fl := &fakeFlag{ID: f.id("flag"), Key: key, Type: typ, Envs: map[string]*fakeEnv{}}
	if typ == "Boolean" && len(variations) == 0 {
		variations = []api.Variation{{Key: "true", Name: "True", Value: "true"}, {Key: "false", Name: "False", Value: "false"}}
	}
	for _, v := range variations {
		v.ID = f.id("var")
		fl.Variations = append(fl.Variations, v)
	}
	deflt := fl.Variations[0].ID
	for _, v := range fl.Variations {
		if typ == "Boolean" && v.Key == "false" {
			deflt = v.ID
		}
	}
	for _, e := range f.envs {
		fl.Envs[e] = &fakeEnv{Default: deflt, Strategy: "SingleVariation"}
	}
	f.flags[key] = fl
	return fl
}

func (f *fakeTarget) createFlag(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		f.t.Errorf("flag create sent no Idempotency-Key")
	}
	if prev, ok := f.idem[key]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		w.Write(prev)
		return
	}
	var req api.CreateFlagRequest
	json.Unmarshal(body, &req)
	if _, exists := f.flags[req.Key]; exists {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"validation_failed","message":"A feature flag with this key already exists in this project.","docs_url":"d"}`)
		return
	}
	var vars []api.Variation
	for _, v := range req.InitialVariations {
		vars = append(vars, api.Variation{Key: v.Key, Name: v.Name, Value: v.Value})
	}
	fl := f.addFlag(req.Key, req.Type, vars...)
	fl.ClientSide = req.ClientSideVisible
	out, _ := json.Marshal(f.flagResponse(fl))
	f.idem[key] = out
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	w.Write(out)
}

func (f *fakeTarget) flagResponse(fl *fakeFlag) api.Flag {
	return api.Flag{ID: fl.ID, Key: fl.Key, Name: fl.Key, Type: fl.Type, IsArchived: fl.Archived, Variations: fl.Variations}
}

func (f *fakeTarget) lookupFlag(w http.ResponseWriter, r *http.Request) *fakeFlag {
	fl := f.flags[r.PathValue("flag")]
	if fl == nil {
		notFound(w)
	}
	return fl
}

func (f *fakeTarget) getFlag(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl := f.lookupFlag(w, r); fl != nil {
		f.writeJSON(w, 200, f.flagResponse(fl))
	}
}

func (f *fakeTarget) listFlagEnvs(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl := f.lookupFlag(w, r)
	if fl == nil {
		return
	}
	var items []api.FlagEnvConfig
	for _, e := range f.envs {
		fe := fl.Envs[e]
		items = append(items, api.FlagEnvConfig{EnvironmentID: "env-" + e, EnvironmentKey: e, IsEnabled: fe.Enabled, DefaultVariationID: fe.Default, Strategy: fe.Strategy, Prerequisites: fe.Prereqs})
	}
	f.writeJSON(w, 200, items) // a bare array, as the API returns it
}

func (f *fakeTarget) lookupEnv(w http.ResponseWriter, r *http.Request) *fakeEnv {
	fl := f.lookupFlag(w, r)
	if fl == nil {
		return nil
	}
	fe := fl.Envs[r.PathValue("env")]
	if fe == nil {
		notFound(w)
	}
	return fe
}

func (f *fakeTarget) putFlagEnv(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fe := f.lookupEnv(w, r)
	if fe == nil {
		return
	}
	var req api.UpdateFlagEnvConfigRequest
	json.NewDecoder(r.Body).Decode(&req)
	fe.Default = req.DefaultVariationID
	if req.Strategy != nil {
		fe.Strategy = *req.Strategy
	}
	if req.Prerequisites != nil {
		// Like the API, refuse a prerequisite that doesn't exist yet, so the
		// order flags are written in is tested.
		for _, pr := range *req.Prerequisites {
			pf, ok := f.flags[pr.PrerequisiteFlagKey]
			if !ok || !hasVariation(pf.Variations, pr.ExpectedVariationKey) {
				w.WriteHeader(422)
				io.WriteString(w, `{"error":"validation_failed","message":"unknown prerequisite","docs_url":"d"}`)
				return
			}
		}
		fe.Prereqs = *req.Prerequisites
	}
	w.WriteHeader(204)
}

func (f *fakeTarget) getTargeting(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fe := f.lookupEnv(w, r); fe != nil {
		rules := fe.Rules
		if rules == nil {
			rules = []api.TargetingRule{}
		}
		f.writeJSON(w, 200, api.TargetingConfig{IsEnabled: fe.Enabled, Rules: rules})
	}
}

// putTargeting stores the rules the way the API returns them: every rule has
// an id and a bucketBy, and a segment rule has no condition groups.
func (f *fakeTarget) putTargeting(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fe := f.lookupEnv(w, r)
	if fe == nil {
		return
	}
	var req api.PutTargetingRequest
	json.NewDecoder(r.Body).Decode(&req)
	fe.Rules = nil
	for _, rr := range req.Rules {
		rule := api.TargetingRule{
			ID: f.id("rule"), Description: rr.Description, VariationID: rr.VariationID,
			RolloutPercentage: rr.RolloutPercentage, UserSegmentID: rr.UserSegmentID,
			ConditionGroups: rr.ConditionGroups, BucketBy: "user_id",
		}
		if rr.BucketBy != nil {
			rule.BucketBy = *rr.BucketBy
		}
		if rule.ConditionGroups == nil {
			rule.ConditionGroups = []api.ConditionGroup{}
		}
		// The API doesn't keep the order of a group's conditions.
		for _, g := range rule.ConditionGroups {
			slices.Reverse(g.Conditions)
		}
		fe.Rules = append(fe.Rules, rule)
	}
	w.WriteHeader(204)
}

func (f *fakeTarget) toggle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fe := f.lookupEnv(w, r)
	if fe == nil {
		return
	}
	var req api.ToggleRequest
	json.NewDecoder(r.Body).Decode(&req)
	fe.Enabled = *req.Enabled
	w.WriteHeader(200)
	io.WriteString(w, `{}`)
}

func (f *fakeTarget) listSegments(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := []api.Segment{}
	for _, k := range sortedKeys(f.segments) {
		items = append(items, *f.segments[k])
	}
	f.writeJSON(w, 200, api.Page[api.Segment]{Items: items})
}

func (f *fakeTarget) createSegment(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var req api.CreateSegmentRequest
	json.NewDecoder(r.Body).Decode(&req)
	slices.Reverse(req.Conditions) // nor a segment's
	sg := &api.Segment{ID: f.id("seg"), Key: req.Key, Name: req.Name, Description: req.Description, Conditions: req.Conditions}
	f.segments[req.Key] = sg
	f.writeJSON(w, 201, sg)
}

func (f *fakeTarget) putSegment(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sg := f.segments[r.PathValue("segment")]
	if sg == nil {
		notFound(w)
		return
	}
	var req api.UpdateSegmentRequest
	json.NewDecoder(r.Body).Decode(&req)
	sg.Name, sg.Description, sg.Conditions = req.Name, req.Description, req.Conditions
	f.writeJSON(w, 200, sg)
}

func (f *fakeTarget) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
