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
	EnvironmentID      string `json:"environmentId"`
	EnvironmentKey     string `json:"environmentKey"`
	IsEnabled          bool   `json:"isEnabled"`
	DefaultVariationID string `json:"defaultVariationId"`
	Strategy           string `json:"strategy"`
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
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
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
