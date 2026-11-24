package importer

import (
	"slices"
	"sort"
	"strings"
)

// Severity says what a translation problem does to the item it is about.
type Severity string

const (
	// Error blocks the item: it is not written, because what was imported
	// would behave differently from the source.
	Error Severity = "error"
	// Warning imports the item, with something the reader must act on or know.
	Warning Severity = "warning"
	// Info is context: what was skipped on purpose, or how a value was read.
	Info Severity = "info"
)

// Kind is what an Issue is about.
type Kind string

const (
	KindFlag    Kind = "flag"
	KindSegment Kind = "segment"
	KindProject Kind = "project"
)

// Issue is one translation decision the reader needs to see. Subject is the
// item's key in the source; Environments lists the Featureflip environments
// it applies to, empty when it applies to the item as a whole.
type Issue struct {
	Severity     Severity `json:"severity"`
	Kind         Kind     `json:"kind"`
	Subject      string   `json:"subject"`
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	Environments []string `json:"environments,omitempty"`
}

// Report collects issues. Adding the same issue (kind, subject, code and
// message) again only adds its environment, so a rule repeated in three
// environments reads as one line.
type Report struct {
	issues []*Issue
	index  map[string]*Issue
}

func NewReport() *Report { return &Report{index: map[string]*Issue{}} }

// Add records an issue. env may be "" for one about the whole item.
func (r *Report) Add(sev Severity, kind Kind, subject, code, msg, env string) {
	id := string(kind) + "\x00" + subject + "\x00" + code + "\x00" + msg
	is, ok := r.index[id]
	if !ok {
		is = &Issue{Severity: sev, Kind: kind, Subject: subject, Code: code, Message: msg}
		r.index[id] = is
		r.issues = append(r.issues, is)
	}
	if env != "" && !slices.Contains(is.Environments, env) {
		is.Environments = append(is.Environments, env)
	}
}

// Issues returns every issue, most severe first, then in the order added.
func (r *Report) Issues() []Issue {
	out := make([]Issue, len(r.issues))
	for i, is := range r.issues {
		out[i] = *is
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Severity) < rank(out[j].Severity) })
	return out
}

// Blocked reports whether the item has an error.
func (r *Report) Blocked(kind Kind, subject string) bool {
	for _, is := range r.issues {
		if is.Kind == kind && is.Subject == subject && is.Severity == Error {
			return true
		}
	}
	return false
}

// Has reports whether the item has an issue of the given severity.
func (r *Report) Has(kind Kind, subject string, sev Severity) bool {
	for _, is := range r.issues {
		if is.Kind == kind && is.Subject == subject && is.Severity == sev {
			return true
		}
	}
	return false
}

func rank(s Severity) int {
	switch s {
	case Error:
		return 0
	case Warning:
		return 1
	}
	return 2
}

// Attributes lists every context attribute the project's rules and segments
// read, including the attributes rollouts bucket by, sorted with user_id first.
func (p *Project) Attributes() []string {
	seen := map[string]bool{}
	add := func(a string) {
		if a != "" {
			seen[a] = true
		}
	}
	addAll := func(cs []Condition) {
		for _, c := range cs {
			add(c.Attribute)
		}
	}
	for _, s := range p.Segments {
		if !s.Blocked {
			addAll(s.Conditions)
		}
	}
	for _, f := range p.Flags {
		if f.Blocked {
			continue
		}
		for _, ec := range f.Environments {
			for _, r := range ec.Rules {
				if r.Rollout != nil {
					add(r.BucketBy)
				}
				for _, g := range r.Groups {
					addAll(g.Conditions)
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i] == UserID) != (out[j] == UserID) {
			return out[i] == UserID
		}
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
