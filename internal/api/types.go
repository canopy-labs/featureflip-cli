package api

// Wire types for the fields the CLI reads or writes. The JSON names are
// checked against the public OpenAPI document by contract_test.go.

type Me struct {
	Type           string   `json:"type"` // "user" or "service_token"
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Email          *string  `json:"email,omitempty"`
	EmailVerified  *bool    `json:"emailVerified,omitempty"`
	OrganizationID *string  `json:"organizationId,omitempty"`
	Role           *string  `json:"role,omitempty"`
	ProjectScope   []string `json:"projectScope,omitempty"` // absent = every project
}

type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Plan string `json:"plan"`
	// Limits is nil from a server that predates it.
	Limits *PlanLimits `json:"limits,omitempty"`
}

// PlanLimits are the plan's caps; nil means unlimited. The environment and
// flag caps are per project, and archived flags don't count.
type PlanLimits struct {
	Projects               *int `json:"projects"`
	EnvironmentsPerProject *int `json:"environmentsPerProject"`
	FlagsPerProject        *int `json:"flagsPerProject"`
	Members                *int `json:"members"`
	Webhooks               *int `json:"webhooks"`
}

type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type Environment struct {
	ID                  string `json:"id"`
	Key                 string `json:"key"`
	Name                string `json:"name"`
	SortOrder           int    `json:"sortOrder"`
	RequireChangeReason bool   `json:"requireChangeReason"`
	RequireConfirmation bool   `json:"requireConfirmation"`
}

type FlagListItem struct {
	ID                string   `json:"id"`
	Key               string   `json:"key"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Tags              []string `json:"tags"`
	IsArchived        bool     `json:"isArchived"`
	ClientSideVisible bool     `json:"clientSideVisible"`
}

type Variation struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Flag struct {
	ID                string      `json:"id"`
	Key               string      `json:"key"`
	Name              string      `json:"name"`
	Description       *string     `json:"description"`
	Type              string      `json:"type"`
	Tags              []string    `json:"tags"`
	IsArchived        bool        `json:"isArchived"`
	ClientSideVisible bool        `json:"clientSideVisible"`
	Variations        []Variation `json:"variations"`
}

type FlagEnvConfig struct {
	EnvironmentID      string         `json:"environmentId"`
	EnvironmentKey     string         `json:"environmentKey"`
	IsEnabled          bool           `json:"isEnabled"`
	DefaultVariationID string         `json:"defaultVariationId"`
	Strategy           string         `json:"strategy"`
	Prerequisites      []Prerequisite `json:"prerequisites"`
}

// Prerequisite makes a flag serve its off variation unless another flag
// serves the expected variation.
type Prerequisite struct {
	PrerequisiteFlagKey  string `json:"prerequisiteFlagKey"`
	ExpectedVariationKey string `json:"expectedVariationKey"`
}

type SideSummary struct {
	IsEnabled bool `json:"isEnabled"`
	RuleCount int  `json:"ruleCount"`
}

type ComparisonRow struct {
	FlagKey  string       `json:"flagKey"`
	FlagName string       `json:"flagName"`
	Base     *SideSummary `json:"base"`
	Other    *SideSummary `json:"other"`
}

type Segment struct {
	ID          string             `json:"id"`
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Description *string            `json:"description"`
	Conditions  []SegmentCondition `json:"conditions"`
}

// SegmentCondition is one condition of a segment; a segment ANDs them.
type SegmentCondition struct {
	Attribute string   `json:"attribute"`
	Operator  string   `json:"operator"`
	Values    []string `json:"values"`
	Negate    bool     `json:"negate"`
}

type CreateSegmentRequest struct {
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Description *string            `json:"description,omitempty"`
	Conditions  []SegmentCondition `json:"conditions"`
}

type UpdateSegmentRequest struct {
	Name        string             `json:"name"`
	Description *string            `json:"description"`
	Conditions  []SegmentCondition `json:"conditions"`
}

// Condition is one condition of a targeting rule's condition group.
type Condition struct {
	Attribute string   `json:"attribute"`
	Operator  string   `json:"operator"`
	Values    []string `json:"values"`
	Negate    bool     `json:"negate"`
}

// ConditionGroup combines its conditions with Operator (And or Or); a rule
// ANDs its groups.
type ConditionGroup struct {
	Operator   string      `json:"operator"`
	Conditions []Condition `json:"conditions"`
}

// TargetingConfig is a flag's targeting in one environment.
type TargetingConfig struct {
	IsEnabled bool            `json:"isEnabled"`
	Rules     []TargetingRule `json:"rules"`
}

type TargetingRule struct {
	ID                string           `json:"id"`
	Description       *string          `json:"description"`
	VariationID       string           `json:"variationId"`
	RolloutPercentage *int             `json:"rolloutPercentage"`
	BucketBy          string           `json:"bucketBy"`
	UserSegmentID     *string          `json:"userSegmentId"`
	ConditionGroups   []ConditionGroup `json:"conditionGroups"`
}

// RuleRequest is one rule of a targeting replace.
type RuleRequest struct {
	Description       *string          `json:"description,omitempty"`
	VariationID       string           `json:"variationId"`
	RolloutPercentage *int             `json:"rolloutPercentage,omitempty"`
	UserSegmentID     *string          `json:"userSegmentId,omitempty"`
	ConditionGroups   []ConditionGroup `json:"conditionGroups"`
	BucketBy          *string          `json:"bucketBy,omitempty"`
}

// PutTargetingRequest replaces every rule of a flag in one environment.
type PutTargetingRequest struct {
	Rules []RuleRequest `json:"rules"`
}

type UpdateFlagEnvConfigRequest struct {
	DefaultVariationID string  `json:"defaultVariationId"`
	Strategy           *string `json:"strategy,omitempty"`
	// Prerequisites replaces the flag's prerequisites when non-nil; nil
	// leaves them as they are.
	Prerequisites *[]Prerequisite `json:"prerequisites,omitempty"`
}

type InitialVariation struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CreateFlagRequest struct {
	Key               string             `json:"key"`
	Name              string             `json:"name"`
	Type              string             `json:"type"`
	Description       *string            `json:"description,omitempty"`
	Tags              []string           `json:"tags,omitempty"`
	InitialVariations []InitialVariation `json:"initialVariations,omitempty"`
	ClientSideVisible bool               `json:"clientSideVisible"`
}

type ToggleRequest struct {
	Enabled *bool `json:"enabled"`
}
