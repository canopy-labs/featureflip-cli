package launchdarkly

import (
	"fmt"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// prerequisites translates a flag's prerequisites in every environment.
func (t *translator) prerequisites(f flag, out *im.Flag) {
	for _, e := range t.envs {
		cfg, ec := f.Environments[e.ld], out.Environments[e.key]
		if cfg == nil || ec == nil {
			continue
		}
		for _, pr := range cfg.Prerequisites {
			t.prereqSrc[f.Key] = appendNew(t.prereqSrc[f.Key], pr.Key)
			p := t.flags[pr.Key]
			if p == nil {
				t.rep.Add(im.Error, im.KindFlag, f.Key, "prerequisite-blocked",
					fmt.Sprintf("its prerequisite %q isn't imported: it is archived, or can't be read", pr.Key), e.key)
				continue
			}
			if pr.Variation < 0 || pr.Variation >= len(p.keys) {
				t.rep.Add(im.Error, im.KindFlag, f.Key, "prerequisite-blocked",
					fmt.Sprintf("its prerequisite %q requires variation %d, which that flag doesn't have", pr.Key, pr.Variation), e.key)
				continue
			}
			want := p.keys[pr.Variation]
			ec.Prerequisites = append(ec.Prerequisites, im.Prerequisite{Flag: t.flagKey[pr.Key], Variation: want})
			if p.off[e.key] == want {
				t.rep.Add(im.Warning, im.KindFlag, f.Key, "prerequisite-off", fmt.Sprintf(
					"LaunchDarkly fails a prerequisite that is off, and Featureflip passes it, because %q serves %q while it is off, the variation this flag requires",
					pr.Key, want), e.key)
			}
		}
	}
}

// blockDependents blocks every flag whose prerequisites are blocked, repeating
// until whole chains are, and every flag on a prerequisite cycle.
func (t *translator) blockDependents(flags []*im.Flag) {
	const visiting, done = 1, 2
	state := map[string]int{}
	var visit func(src string)
	visit = func(src string) {
		state[src] = visiting
		for _, dep := range t.prereqSrc[src] {
			switch state[dep] {
			case visiting:
				t.rep.Add(im.Error, im.KindFlag, src, "prerequisite-cycle",
					fmt.Sprintf("it and %q depend on each other through prerequisites", dep), "")
				t.rep.Add(im.Error, im.KindFlag, dep, "prerequisite-cycle",
					fmt.Sprintf("it and %q depend on each other through prerequisites", src), "")
			case 0:
				visit(dep)
			}
		}
		state[src] = done
	}
	for _, f := range flags {
		if state[f.SourceKey] == 0 {
			visit(f.SourceKey)
		}
	}

	for changed := true; changed; {
		changed = false
		for _, f := range flags {
			if t.rep.Blocked(im.KindFlag, f.SourceKey) {
				continue
			}
			for _, dep := range t.prereqSrc[f.SourceKey] {
				if t.flags[dep] != nil && t.rep.Blocked(im.KindFlag, dep) {
					t.rep.Add(im.Error, im.KindFlag, f.SourceKey, "prerequisite-blocked",
						fmt.Sprintf("its prerequisite %q is blocked, so it can't be imported either", dep), "")
					changed = true
					break
				}
			}
		}
	}
}

func appendNew(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}
