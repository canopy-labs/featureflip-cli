package importer

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Counts tallies one kind of item by outcome. Warned items are also Ready.
type Counts struct {
	Read    int `json:"read"`
	Ready   int `json:"ready"`
	Warned  int `json:"withWarnings"`
	Blocked int `json:"blocked"`
}

type Summary struct {
	Flags    Counts `json:"flags"`
	Segments Counts `json:"segments"`
}

// Summarize marks blocked items from the report and counts every outcome.
func (res *Result) Summarize() Summary {
	var s Summary
	p, r := res.Project, res.Report
	for i := range p.Flags {
		f := &p.Flags[i]
		f.Blocked = r.Blocked(KindFlag, f.SourceKey)
		tally(&s.Flags, f.Blocked, r.Has(KindFlag, f.SourceKey, Warning))
	}
	for i := range p.Segments {
		sg := &p.Segments[i]
		sg.Blocked = r.Blocked(KindSegment, sg.SourceKey)
		tally(&s.Segments, sg.Blocked, r.Has(KindSegment, sg.SourceKey, Warning))
	}
	return s
}

func tally(c *Counts, blocked, warned bool) {
	c.Read++
	switch {
	case blocked:
		c.Blocked++
	case warned:
		c.Ready++
		c.Warned++
	default:
		c.Ready++
	}
}

// Document is the --json output of a dry run.
type Document struct {
	Source     string   `json:"source"`
	DryRun     bool     `json:"dryRun"`
	Summary    Summary  `json:"summary"`
	Attributes []string `json:"attributes"`
	Issues     []Issue  `json:"issues"`
	Project    *Project `json:"project"`
	// Target is what --apply did to the Featureflip project.
	Target *Plan `json:"target,omitempty"`
}

func (res *Result) Document() Document {
	sum := res.Summarize()
	issues := res.Report.Issues()
	if issues == nil {
		issues = []Issue{}
	}
	return Document{
		Source:     res.Source,
		DryRun:     true,
		Summary:    sum,
		Attributes: res.Project.Attributes(),
		Issues:     issues,
		Project:    res.Project,
	}
}

// ApplyDocument is the --json output of an import that wrote (or tried to
// write) the plan.
func (res *Result) ApplyDocument(plan *Plan) Document {
	doc := res.Document()
	doc.DryRun = false
	doc.Target = plan
	return doc
}

// RenderText writes the dry-run report for a person.
func (res *Result) RenderText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s import: dry run, nothing was written.\n\n", res.Source)
	res.renderReport(&b)
	_, err := io.WriteString(w, b.String())
	return err
}

// RenderApplied writes the report followed by what the import wrote to the
// Featureflip project, which is only part of the plan when a write failed.
func (res *Result) RenderApplied(w io.Writer, plan *Plan) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s import into %s/%s.\n\n", res.Source, plan.Org, plan.Project)
	res.renderReport(&b)

	b.WriteString("\nWritten to Featureflip\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, kind := range []Kind{KindSegment, KindFlag} {
		fmt.Fprintf(tw, "  %s\t%s\n", plural(kind), outcomeCounts(plan, kind))
	}
	tw.Flush()

	var untouched []string
	for _, st := range plan.Steps {
		if st.Action == ActDiffers || st.Action == ActConflict {
			envs := ""
			if st.Action == ActDiffers && len(st.Environments) > 0 {
				envs = fmt.Sprintf(" [%s]", strings.Join(st.Environments, ", "))
			}
			untouched = append(untouched, fmt.Sprintf("  %s %s: %s%s\n", st.Kind, st.Key, st.Why, envs))
		}
	}
	if len(untouched) > 0 {
		b.WriteString("\nLeft untouched\n")
		for _, line := range untouched {
			b.WriteString(line)
		}
	}
	for _, st := range plan.Steps {
		if st.Created && !st.Done {
			fmt.Fprintf(&b, "\nflag %s was created but not fully configured. Re-run the import with --overwrite to finish it;\n"+
				"without it, the next run reads the flag as differing and leaves it as it is.\n", st.Key)
		}
	}
	if plan.wroteFlags() && !plan.opts.CarryState {
		fmt.Fprintf(&b, "\nImported flags are off in every environment. Turn them on when your app sends the context attributes above,\n"+
			"or re-run with --carry-state to copy each environment's on/off state from %s.\n", res.Source)
	}
	if plan.wroteFlags() && !plan.opts.ClientSide {
		b.WriteString("\nImported flags are server-side only. Mark the ones your browser or mobile apps read as client-side\n" +
			"in the dashboard (an import that creates flags does this for all of them with --client-side).\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func plural(k Kind) string {
	if k == KindSegment {
		return "Segments"
	}
	return "Flags"
}

// outcomeCounts summarizes one kind's steps, counting a create or update as
// done only once it was written.
func outcomeCounts(plan *Plan, kind Kind) string {
	order := []struct {
		label string
		match func(*Step) bool
	}{
		{"created", func(s *Step) bool { return s.Action == ActCreate && s.Done }},
		{"updated", func(s *Step) bool { return s.Action == ActUpdate && s.Done }},
		{"unchanged", func(s *Step) bool { return s.Action == ActUnchanged }},
		{"created but not fully configured (stopped at an error)", func(s *Step) bool { return s.Created && !s.Done }},
		{"not written (stopped at an error)", func(s *Step) bool { return s.writes() && !s.Done && !s.Created }},
		{"left untouched", func(s *Step) bool { return s.Action == ActDiffers || s.Action == ActConflict }},
		{"blocked", func(s *Step) bool { return s.Action == ActBlocked }},
	}
	var parts []string
	for _, o := range order {
		n := 0
		for _, st := range plan.Steps {
			if st.Kind == kind && o.match(st) {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, o.label))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func (p *Plan) wroteFlags() bool {
	for _, st := range p.Steps {
		if st.Kind == KindFlag && st.Created {
			return true
		}
	}
	return false
}

func (res *Result) renderReport(b *strings.Builder) {
	doc := res.Document()
	p := res.Project

	b.WriteString("Environments\n")
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	for _, e := range p.Environments {
		fmt.Fprintf(tw, "  %s\t-> %s\n", e.Source, e.Key)
	}
	tw.Flush()

	b.WriteString("\n")
	tw = tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Flags\t%s\n", counts(doc.Summary.Flags))
	fmt.Fprintf(tw, "Segments\t%s\n", counts(doc.Summary.Segments))
	tw.Flush()

	sections := []struct {
		sev   Severity
		title string
	}{
		{Error, "Blocked: these are not imported"},
		{Warning, "Warnings: imported, but check these"},
		{Info, "Notes"},
	}
	for _, sec := range sections {
		var lines []Issue
		for _, is := range doc.Issues {
			if is.Severity == sec.sev {
				lines = append(lines, is)
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(b, "\n%s\n", sec.title)
		for _, is := range lines {
			subject := ""
			if is.Kind != KindProject {
				subject = fmt.Sprintf("%s %s: ", is.Kind, is.Subject)
			}
			envs := ""
			if len(is.Environments) > 0 && len(is.Environments) < len(p.Environments) {
				envs = fmt.Sprintf(" [%s]", strings.Join(is.Environments, ", "))
			}
			fmt.Fprintf(b, "  %s%s%s\n", subject, is.Message, envs)
		}
	}

	if len(doc.Attributes) > 0 {
		b.WriteString("\nContext attributes the imported rules read (your evaluation context must carry them):\n")
		fmt.Fprintf(b, "  %s\n", strings.Join(doc.Attributes, ", "))
	}
}

func counts(c Counts) string {
	if c.Read == 0 {
		return "none found"
	}
	s := fmt.Sprintf("%d read: %d ready", c.Read, c.Ready)
	if c.Warned > 0 {
		s += fmt.Sprintf(" (%d with warnings)", c.Warned)
	}
	if c.Blocked > 0 {
		s += fmt.Sprintf(", %d blocked", c.Blocked)
	}
	return s
}
