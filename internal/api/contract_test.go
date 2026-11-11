package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The OpenAPI snapshot exists only in the monorepo this package is developed
// in; the published copy of this module has no such file and skips the test.
func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "apps", "management-api", "public-v1-openapi.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skip("public OpenAPI snapshot not present")
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestContractRoutesExist(t *testing.T) {
	doc := loadOpenAPI(t)
	paths := doc["paths"].(map[string]any)
	if len(Routes) == 0 {
		t.Fatal("no routes declared")
	}
	for _, r := range Routes {
		item, ok := paths[r.Template].(map[string]any)
		if !ok {
			t.Errorf("%s %s: no such path", r.Method, r.Template)
			continue
		}
		op, ok := item[strings.ToLower(r.Method)].(map[string]any)
		if !ok {
			t.Errorf("%s %s: no such method", r.Method, r.Template)
			continue
		}
		declared := map[string]bool{}
		params, _ := op["parameters"].([]any)
		for _, p := range params {
			pm := p.(map[string]any)
			declared[pm["in"].(string)+":"+pm["name"].(string)] = true
		}
		for _, q := range r.Query {
			if !declared["query:"+q] {
				t.Errorf("%s %s: no query parameter %q", r.Method, r.Template, q)
			}
		}
		for _, h := range r.Headers {
			if !declared["header:"+h] {
				t.Errorf("%s %s: no header parameter %q", r.Method, r.Template, h)
			}
		}
	}
}

func TestContractDTOFieldsExist(t *testing.T) {
	doc := loadOpenAPI(t)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	cases := []struct {
		value  any
		schema string
	}{
		{Me{}, "MeResponse"},
		{Org{}, "OrganizationResponse"},
		{Project{}, "ProjectResponse"},
		{Environment{}, "PublicEnvironmentResponse"},
		{FlagListItem{}, "PublicFlagListItem"},
		{Flag{}, "PublicFlagResponse"},
		{Variation{}, "PublicVariationResponse"},
		{FlagEnvConfig{}, "PublicFlagEnvConfigResponse"},
		{ComparisonRow{}, "PublicFlagComparisonRow"},
		{SideSummary{}, "PublicEnvironmentSideSummary"},
		{Segment{}, "PublicSegmentResponse"},
		{CreateFlagRequest{}, "PublicCreateFeatureFlagRequest"},
		{InitialVariation{}, "PublicCreateVariationDto"},
		{ToggleRequest{}, "PublicToggleFlagRequest"},
		{Page[json.RawMessage]{}, "PublicFlagListItemPagedResult"},
		{envelope{}, "PublicApiErrorEnvelope"},
	}
	for _, c := range cases {
		schema, ok := schemas[c.schema].(map[string]any)
		if !ok {
			t.Errorf("no schema %s", c.schema)
			continue
		}
		props, _ := schema["properties"].(map[string]any)
		typ := reflect.TypeOf(c.value)
		for i := 0; i < typ.NumField(); i++ {
			name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			if _, ok := props[name]; !ok {
				t.Errorf("%s.%s: %s has no property %q", typ.Name(), typ.Field(i).Name, c.schema, name)
			}
		}
	}
}
