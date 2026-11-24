package launchdarkly

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

var operators = map[string]im.Operator{
	"in":                 im.OpIn,
	"startsWith":         im.OpStartsWith,
	"endsWith":           im.OpEndsWith,
	"contains":           im.OpContains,
	"matches":            im.OpMatchesRegex,
	"lessThan":           im.OpLessThan,
	"lessThanOrEqual":    im.OpLessThanOrEqual,
	"greaterThan":        im.OpGreaterThan,
	"greaterThanOrEqual": im.OpGreaterThanOrEqual,
	"before":             im.OpBefore,
	"after":              im.OpAfter,
	"semVerEqual":        im.OpSemverEquals,
	"semVerLessThan":     im.OpSemverLess,
	"semVerGreaterThan":  im.OpSemverGreater,
}

// looseOperators match more loosely in Featureflip than in LaunchDarkly: the
// string ones ignore case, and the numeric ones read a numeric string as a
// number, which LaunchDarkly never does.
var looseOperators = map[string]bool{
	"in": true, "startsWith": true, "endsWith": true, "contains": true,
	"lessThan": true, "lessThanOrEqual": true, "greaterThan": true, "greaterThanOrEqual": true,
}

var strictSemver = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+].*)?$`)

type clauseResult struct {
	conds  []im.Condition
	always bool // the clause holds for every context (drop it)
	never  bool // the clause holds for no context
	code   string
	err    string
}

// clause translates one LaunchDarkly clause into ANDed Featureflip conditions.
func (t *translator) clause(c clause, emit func(im.Severity, string, string)) clauseResult {
	attr, code, err := attribute(c.ContextKind, c.Attribute)
	if err != "" {
		return clauseResult{code: code, err: err}
	}
	t.noteKind(c.ContextKind)
	op, ok := operators[c.Op]
	if !ok {
		return clauseResult{code: "unsupported-operator", err: fmt.Sprintf("it compares %q with the operator %s, which has no Featureflip equivalent", attr, c.Op)}
	}
	values := make([]string, 0, len(c.Values))
	for _, raw := range c.Values {
		v, ok := clauseValue(raw)
		if !ok {
			return clauseResult{code: "unreadable-condition", err: fmt.Sprintf("its %q condition compares against %s, which isn't a string, number or boolean", attr, strings.TrimSpace(string(raw)))}
		}
		values = append(values, v)
	}
	if len(values) == 0 {
		// Matching any of no values matches nothing.
		if c.Negate {
			return clauseResult{always: true}
		}
		return clauseResult{never: true}
	}

	if looseOperators[c.Op] {
		t.loose = true
	}
	switch c.Op {
	case "matches":
		emit(im.Info, "regex-dialect", fmt.Sprintf("its %q pattern is run by .NET's regular expressions in Featureflip and by Go's in LaunchDarkly; common patterns behave the same", attr))
	case "before", "after":
		for i, raw := range c.Values {
			if jsonKind(raw) == "number" {
				if ms, err := strconv.ParseInt(values[i], 10, 64); err == nil {
					values[i] = time.UnixMilli(ms).UTC().Format(time.RFC3339)
				}
			}
		}
		emit(im.Warning, "date-attribute",
			fmt.Sprintf("Featureflip reads the date in %q as an ISO 8601 date or Unix seconds; LaunchDarkly also accepts Unix milliseconds, which apps often send", attr))
	case "semVerEqual", "semVerLessThan", "semVerGreaterThan":
		for _, v := range values {
			if !strictSemver.MatchString(v) {
				emit(im.Warning, "version-format",
					fmt.Sprintf("it compares %q with the version %q; Featureflip compares full semantic versions (major.minor.patch)", attr, v))
				break
			}
		}
	}

	conds := []im.Condition{{Attribute: attr, Operator: op, Values: values, Negate: c.Negate}}
	// A negated Featureflip condition matches a context without the
	// attribute, and a negated LaunchDarkly clause doesn't: require it to be
	// there. Every context has a key, so user_id needs no guard.
	if c.Negate && attr != im.UserID {
		conds = append(conds, presence(attr))
	}
	return clauseResult{conds: conds}
}

// presence matches every context that has the attribute, whatever its value.
func presence(attr string) im.Condition {
	return im.Condition{Attribute: attr, Operator: im.OpMatchesRegex, Values: []string{"^"}}
}

// clauseValue renders a clause value the way Featureflip compares it.
func clauseValue(raw json.RawMessage) (string, bool) {
	switch jsonKind(raw) {
	case "string":
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	case "number", "bool":
		return strings.TrimSpace(string(raw)), true
	}
	return "", false
}

// attribute names the flat Featureflip attribute a LaunchDarkly attribute of a
// context kind becomes: a user's attributes keep their names and its key is
// user_id; another kind's attributes are prefixed with the kind.
func attribute(kind, attr string) (name, code, err string) {
	if strings.HasPrefix(attr, "/") {
		ref := attr[1:]
		if strings.Contains(ref, "/") {
			return "", "nested-attribute", fmt.Sprintf("it reads the nested attribute %s, and Featureflip contexts are flat", attr)
		}
		attr = strings.NewReplacer("~1", "/", "~0", "~").Replace(ref)
	}
	switch attr {
	case "":
		return "", "unreadable-condition", "it names no attribute"
	case "kind":
		return "", "context-kind-clause", "it checks the context kind, and Featureflip contexts have no kinds"
	}
	if kind == "" || kind == "user" {
		if attr == "key" {
			return im.UserID, "", ""
		}
		return attr, "", ""
	}
	return kind + "." + attr, "", ""
}
