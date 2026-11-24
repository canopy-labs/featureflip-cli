package statsig

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Wire types for the parts of the Statsig Console API (version 20240601) the
// importer reads. Fields it doesn't use are left out.

type environment struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	IsProduction bool   `json:"isProduction"`
}

type environmentsData struct {
	Environments []environment `json:"environments"`
}

type condition struct {
	Type        string          `json:"type"`
	Operator    string          `json:"operator"`
	TargetValue json.RawMessage `json:"targetValue"`
	Field       *string         `json:"field"`
	CustomID    *string         `json:"customID"`
}

type automatedRollout struct {
	Time        float64 `json:"time"`
	PassPercent float64 `json:"passPercent"`
}

type variant struct {
	Name           string  `json:"name"`
	PassPercentage float64 `json:"passPercentage"`
}

type rule struct {
	ID                       string             `json:"id"`
	Name                     string             `json:"name"`
	PassPercentage           float64            `json:"passPercentage"`
	Conditions               []condition        `json:"conditions"`
	Environments             []string           `json:"environments"`
	ReturnValue              json.RawMessage    `json:"returnValue"`
	PendingAutomatedRollouts []automatedRollout `json:"pendingAutomatedRollouts"`
	Variants                 []variant          `json:"variants"`
}

type gate struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	IDType      string       `json:"idType"`
	Description string       `json:"description"`
	Tags        []string     `json:"tags"`
	TargetApps  stringOrList `json:"targetApps"`
	HoldoutIDs  []string     `json:"holdoutIDs"`
	Status      string       `json:"status"`
	Type        string       `json:"type"`
	IsTemplate  bool         `json:"isTemplate"`
	IsEnabled   bool         `json:"isEnabled"`
	Rules       []rule       `json:"rules"`
}

type dynamicConfig struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	IDType       string          `json:"idType"`
	Description  string          `json:"description"`
	Tags         []string        `json:"tags"`
	TargetApps   stringOrList    `json:"targetApps"`
	HoldoutIDs   []string        `json:"holdoutIDs"`
	Status       string          `json:"status"`
	Type         string          `json:"type"`
	IsTemplate   bool            `json:"isTemplate"`
	IsEnabled    bool            `json:"isEnabled"`
	Rules        []rule          `json:"rules"`
	DefaultValue json.RawMessage `json:"defaultValue"`
	Schema       *string         `json:"schema"`
}

type segment struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	IDType      string   `json:"idType"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	IsEnabled   bool     `json:"isEnabled"`
	Count       *int     `json:"count"`
	Rules       []rule   `json:"rules"`
	Tags        []string `json:"tags"`
}

// idList is a page of an ID-list segment's members.
type idList struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// environmentOverride is one environment's (or, with a null environment,
// every environment's) passing and failing IDs for one unit type.
type environmentOverride struct {
	Environment *string  `json:"environment"`
	UnitID      string   `json:"unitID"`
	PassingIDs  []string `json:"passingIDs"`
	FailingIDs  []string `json:"failingIDs"`
}

type overrides struct {
	PassingUserIDs       []string              `json:"passingUserIDs"`
	FailingUserIDs       []string              `json:"failingUserIDs"`
	PassingCustomIDs     []string              `json:"passingCustomIDs"`
	FailingCustomIDs     []string              `json:"failingCustomIDs"`
	EnvironmentOverrides []environmentOverride `json:"environmentOverrides"`
}

// stringOrList decodes a field the API sends as either a string or a list.
type stringOrList []string

func (s *stringOrList) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = nil
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one == "" {
			*s = nil
		} else {
			*s = []string{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// targetValues reads a condition's targetValue, which the API sends as a
// string, a number, a boolean, a list of those, or null.
func targetValues(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		v, err := scalar(raw)
		if err != nil {
			return nil, err
		}
		return []string{v}, nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if string(item) == "null" {
			continue
		}
		v, err := scalar(item)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func scalar(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	switch t := v.(type) {
	case string:
		return t, nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	}
	return "", fmt.Errorf("unsupported target value %s", raw)
}
