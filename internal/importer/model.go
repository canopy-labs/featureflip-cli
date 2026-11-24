// Package importer is the vendor-neutral half of `featureflip import`: the
// model a source vendor's project is translated into, the report that records
// every translation decision, and the renderers. Each vendor lives in its own
// subpackage and implements Source.
//
// The model is shaped like the public API (/api/v1), so writing it is a
// straight mapping: rules hold condition groups ANDed together, a served
// variation and an optional whole-number rollout percentage.
package importer

import (
	"context"

	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
)

// Source reads one vendor's project and translates it. Translation problems
// are recorded in the Report, never returned as an error: an error means the
// source could not be read at all.
type Source interface {
	// Name is the vendor's display name, e.g. "Statsig".
	Name() string
	Load(ctx context.Context, opts Options) (*Result, error)
}

// Options are the translation choices common to every source.
type Options struct {
	// EnvMap renames source environments to Featureflip environment keys.
	// An environment it doesn't name keeps its own name, normalized to a key.
	EnvMap map[string]string
}

// OptionError means the options don't fit the source project, e.g. an
// --env-map naming an environment it doesn't have. Exit code 2.
type OptionError struct{ Msg string }

func (e OptionError) Error() string { return e.Msg }
func (OptionError) ExitCode() int   { return exitcode.Usage }

// Result is a translated project plus the report on how it was translated.
type Result struct {
	Source  string   `json:"source"`
	Project *Project `json:"project"`
	Report  *Report  `json:"-"`
}

// FlagType is a Featureflip flag type, spelled as the API spells it.
type FlagType string

const (
	Boolean FlagType = "Boolean"
	String  FlagType = "String"
	Number  FlagType = "Number"
	JSON    FlagType = "Json"
)

// Operator is a Featureflip condition operator, spelled as the API spells it.
type Operator string

const (
	OpIn                 Operator = "In"
	OpNotIn              Operator = "NotIn"
	OpContains           Operator = "Contains"
	OpNotContains        Operator = "NotContains"
	OpGreaterThan        Operator = "GreaterThan"
	OpGreaterThanOrEqual Operator = "GreaterThanOrEqual"
	OpLessThan           Operator = "LessThan"
	OpLessThanOrEqual    Operator = "LessThanOrEqual"
	OpMatchesRegex       Operator = "MatchesRegex"
	OpStartsWith         Operator = "StartsWith"
	OpEndsWith           Operator = "EndsWith"
	OpBefore             Operator = "Before"
	OpAfter              Operator = "After"
	OpSemverEquals       Operator = "SemverEquals"
	OpSemverGreater      Operator = "SemverGreaterThan"
	OpSemverGreaterEq    Operator = "SemverGreaterThanOrEqual"
	OpSemverLess         Operator = "SemverLessThan"
	OpSemverLessEq       Operator = "SemverLessThanOrEqual"
)

// GroupOperator combines the conditions inside one condition group.
type GroupOperator string

const (
	And GroupOperator = "And"
	Or  GroupOperator = "Or"
)

// UserID is the context attribute that holds the user's id.
const UserID = "user_id"

type Project struct {
	Environments []Environment `json:"environments"`
	Segments     []Segment     `json:"segments"`
	Flags        []Flag        `json:"flags"`
}

// Environment pairs a source environment with the Featureflip key it maps to.
type Environment struct {
	Key    string `json:"key"`
	Source string `json:"source"`
}

type Segment struct {
	Key         string      `json:"key"`
	SourceKey   string      `json:"sourceKey"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Conditions  []Condition `json:"conditions"`
	// Blocked is true when the report holds an error for this segment.
	Blocked bool `json:"blocked"`
}

type Flag struct {
	Key         string      `json:"key"`
	SourceKey   string      `json:"sourceKey"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Type        FlagType    `json:"type"`
	Tags        []string    `json:"tags,omitempty"`
	Variations  []Variation `json:"variations"`
	// ClientSide makes the flag available to client-side SDKs when it is
	// created, as --client-side does for every flag.
	ClientSide bool `json:"clientSide,omitempty"`
	// Environments holds one configuration per Project.Environments entry,
	// keyed by the Featureflip environment key.
	Environments map[string]*EnvConfig `json:"environments"`
	// Blocked is true when the report holds an error for this flag. A blocked
	// flag is never written.
	Blocked bool `json:"blocked"`
}

// Variation's Value is the JSON text of the value, as the API stores it.
type Variation struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type EnvConfig struct {
	// Enabled is the flag's state in the source. Whether it is carried over
	// is the writer's decision.
	Enabled bool   `json:"enabled"`
	Rules   []Rule `json:"rules"`
	// Default is the variation served when no rule matches.
	Default string `json:"default"`
	// Prerequisites must all serve their variation for the rules to apply.
	Prerequisites []Prerequisite `json:"prerequisites,omitempty"`
}

// Prerequisite is another imported flag, by its Featureflip key, and the
// variation key it must serve.
type Prerequisite struct {
	Flag      string `json:"flag"`
	Variation string `json:"variation"`
}

type Rule struct {
	Description string           `json:"description,omitempty"`
	Groups      []ConditionGroup `json:"conditionGroups"`
	// SegmentKey makes the rule match the segment's members. The API then
	// ignores Groups, so a rule carries one or the other, never both.
	SegmentKey string `json:"segment,omitempty"`
	Variation  string `json:"variation"`
	// Rollout, when set (1-99), serves Variation to that share of matching
	// contexts and splits the rest across the flag's other variations.
	Rollout  *int   `json:"rolloutPercentage,omitempty"`
	BucketBy string `json:"bucketBy,omitempty"`
}

type ConditionGroup struct {
	Operator   GroupOperator `json:"operator"`
	Conditions []Condition   `json:"conditions"`
}

type Condition struct {
	Attribute string   `json:"attribute"`
	Operator  Operator `json:"operator"`
	Values    []string `json:"values"`
	Negate    bool     `json:"negate,omitempty"`
}

// Negated returns the condition with its sense flipped.
func (c Condition) Negated() Condition {
	c.Negate = !c.Negate
	c.Values = append([]string(nil), c.Values...)
	return c
}
