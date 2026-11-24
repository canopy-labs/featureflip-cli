package launchdarkly

import "encoding/json"

// The types below decode LaunchDarkly REST API (v2) responses, keeping only
// the fields the importer reads. Variation references are indices into a
// flag's variations.

// snapshot is everything read from one LaunchDarkly project. Translation is a
// pure function of it, so tests drive it from JSON.
type snapshot struct {
	Environments []environment `json:"environments"`
	// Flags are the project's unarchived flags, each carrying the
	// configuration of every environment.
	Flags []flag `json:"flags"`
	// Segments are each environment's segments, by environment key.
	Segments map[string][]segment `json:"segments"`
	// Archived are the keys of the project's archived flags.
	Archived []string `json:"archived"`
}

type environment struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type flag struct {
	Key                    string              `json:"key"`
	Name                   string              `json:"name"`
	Description            string              `json:"description"`
	Kind                   string              `json:"kind"` // boolean or multivariate
	Tags                   []string            `json:"tags"`
	Archived               bool                `json:"archived"`
	Deprecated             bool                `json:"deprecated"`
	Variations             []variation         `json:"variations"`
	ClientSideAvailability *clientSide         `json:"clientSideAvailability"`
	Environments           map[string]*flagEnv `json:"environments"`
}

type clientSide struct {
	UsingEnvironmentID bool `json:"usingEnvironmentId"`
	UsingMobileKey     bool `json:"usingMobileKey"`
}

type variation struct {
	ID          string          `json:"_id"`
	Value       json.RawMessage `json:"value"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
}

// flagEnv is a flag's configuration in one environment.
type flagEnv struct {
	On             bool       `json:"on"`
	Targets        []target   `json:"targets"`
	ContextTargets []target   `json:"contextTargets"`
	Rules          []flagRule `json:"rules"`
	Fallthrough    serve      `json:"fallthrough"`
	// OffVariation is nil when unset: LaunchDarkly then serves the app's
	// fallback value while the flag is off.
	OffVariation  *int     `json:"offVariation"`
	Prerequisites []prereq `json:"prerequisites"`
}

// target lists context keys of one kind that get one variation.
type target struct {
	Values      []string `json:"values"`
	Variation   int      `json:"variation"`
	ContextKind string   `json:"contextKind"`
}

type flagRule struct {
	ID          string   `json:"_id"`
	Description string   `json:"description"`
	Variation   *int     `json:"variation"`
	Rollout     *rollout `json:"rollout"`
	Clauses     []clause `json:"clauses"`
	Disabled    bool     `json:"disabled"`
}

// serve is a fixed variation or a rollout.
type serve struct {
	Variation *int     `json:"variation"`
	Rollout   *rollout `json:"rollout"`
}

type rollout struct {
	Variations  []weighted `json:"variations"`
	BucketBy    string     `json:"bucketBy"`
	ContextKind string     `json:"contextKind"`
	Kind        string     `json:"kind"` // rollout or experiment
}

// weighted is one variation's share of a rollout, in thousandths of a percent.
type weighted struct {
	Variation int `json:"variation"`
	Weight    int `json:"weight"`
}

type clause struct {
	Attribute   string            `json:"attribute"`
	Op          string            `json:"op"`
	Values      []json.RawMessage `json:"values"`
	Negate      bool              `json:"negate"`
	ContextKind string            `json:"contextKind"`
}

type prereq struct {
	Key       string `json:"key"`
	Variation int    `json:"variation"`
}

type segment struct {
	Key              string      `json:"key"`
	Name             string      `json:"name"`
	Description      string      `json:"description"`
	Included         []string    `json:"included"`
	Excluded         []string    `json:"excluded"`
	IncludedContexts []segTarget `json:"includedContexts"`
	ExcludedContexts []segTarget `json:"excludedContexts"`
	Rules            []segRule   `json:"rules"`
	// Unbounded marks a big segment, whose members the API doesn't list.
	Unbounded bool `json:"unbounded"`
	Deleted   bool `json:"deleted"`
}

type segTarget struct {
	ContextKind string   `json:"contextKind"`
	Values      []string `json:"values"`
}

type segRule struct {
	Clauses []clause `json:"clauses"`
	// Weight, when set, includes only that share (in thousandths of a
	// percent) of the contexts the rule matches.
	Weight             *int   `json:"weight"`
	BucketBy           string `json:"bucketBy"`
	RolloutContextKind string `json:"rolloutContextKind"`
}
