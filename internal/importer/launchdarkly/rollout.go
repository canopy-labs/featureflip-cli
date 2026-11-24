package launchdarkly

import (
	"fmt"
	"math"

	im "github.com/canopy-labs/featureflip-cli/internal/importer"
)

// serveOf turns a LaunchDarkly variation or rollout into what a Featureflip
// rule serves. what names it in report messages.
func (t *translator) serveOf(info *flagInfo, v *int, ro *rollout, what string, emit func(im.Severity, string, string)) (im.Rule, bool) {
	var out im.Rule
	switch {
	case v != nil:
		if *v < 0 || *v >= len(info.keys) {
			emit(im.Error, "unreadable-value", fmt.Sprintf("%s serves variation %d, which the flag doesn't have", what, *v))
			return out, false
		}
		out.Variation = info.keys[*v]
		return out, true
	case ro == nil:
		emit(im.Error, "unreadable-value", what+" serves nothing")
		return out, false
	}

	target, pct, exact, ok := split(ro.Variations, len(info.keys), info.boolTrue)
	if !ok {
		emit(im.Error, "uneven-split", fmt.Sprintf(
			"%s splits contexts %s, and a Featureflip rollout serves one variation a percentage and splits the rest evenly across the others",
			what, describeWeights(ro.Variations, info.keys)))
		return out, false
	}
	out.Variation = info.keys[target]
	if pct == 100 {
		return out, true
	}
	kind := ro.ContextKind
	bucketBy := ro.BucketBy
	if bucketBy == "" || ro.Kind == "experiment" {
		bucketBy = "key"
	}
	attr, code, err := attribute(kind, bucketBy)
	if err != "" {
		emit(im.Error, code, fmt.Sprintf("%s buckets by %s: %s", what, bucketBy, err))
		return out, false
	}
	t.noteKind(kind)
	out.Rollout = &pct
	out.BucketBy = attr
	if ro.Kind == "experiment" {
		emit(im.Warning, "experiment", fmt.Sprintf("%s is an experiment in LaunchDarkly; its split is imported, the experiment and its results are not", what))
	}
	if !exact {
		emit(im.Warning, "rollout-rounded", fmt.Sprintf("%s splits contexts %s; Featureflip rollouts are whole percentages, so it serves %s to %d%% and splits the rest evenly",
			what, describeWeights(ro.Variations, info.keys), out.Variation, pct))
	}
	return out, true
}

// split finds the Featureflip rollout closest to LaunchDarkly's weights: one
// target variation at a whole percentage, the rest split evenly across the
// other variations in order, the last taking the remainder. It is ok when
// every variation ends within one point of its LaunchDarkly share, whichever
// of the others takes the remainder; exact when
// every share is the same. boolTrue, the true variation's index (-1 for
// none), is tried first. A single variation with all the weight returns 100.
func split(ws []weighted, n, boolTrue int) (target, pct int, exact, ok bool) {
	w := make([]int, n)
	for _, x := range ws {
		if x.Variation >= 0 && x.Variation < n {
			w[x.Variation] += x.Weight
		}
	}
	nonzero, only := 0, 0
	for i, x := range w {
		if x > 0 {
			nonzero, only = nonzero+1, i
		}
	}
	switch {
	case nonzero == 0 || n < 1:
		return 0, 0, false, false
	case nonzero == 1:
		return only, 100, true, true
	}

	pcts := make([]int, n)
	for i, x := range w {
		pcts[i] = int(math.Round(float64(x) / 1000))
	}
	candidates := []int{}
	if boolTrue >= 0 && boolTrue < n {
		candidates = append(candidates, boolTrue)
	}
	for i := range n {
		if i != boolTrue {
			candidates = append(candidates, i)
		}
	}
	for _, tg := range candidates {
		// Serving a variation LaunchDarkly gives no one would reach contexts
		// LaunchDarkly never sends there.
		if w[tg] == 0 {
			continue
		}
		N := max(1, min(99, pcts[tg]))
		if abs(N-pcts[tg]) > 1 {
			continue
		}
		// Each other variation gets the even share, and one of them the
		// remainder too. Which one depends on the order Featureflip keeps
		// them in, so every one must be close to both.
		rest, k := 100-N, n-1
		even, last := rest/k, rest-rest/k*(k-1)
		good := true
		for j := range n {
			if j != tg && (abs(pcts[j]-even) > 1 || abs(pcts[j]-last) > 1) {
				good = false
				break
			}
		}
		if !good {
			continue
		}
		ff := make([]int, n)
		ff[tg] = N
		others, rest := n-1, 100-N
		seen := 0
		for j := range n {
			if j == tg {
				continue
			}
			seen++
			if seen < others {
				ff[j] = rest / others
			} else {
				ff[j] = rest - rest/others*(others-1)
			}
		}
		exact = true
		for j := range n {
			if ff[j]*1000 != w[j] {
				exact = false
			}
		}
		return tg, N, exact, true
	}
	return 0, 0, false, false
}

func describeWeights(ws []weighted, keys []string) string {
	s := ""
	for i, x := range ws {
		if i > 0 {
			s += ", "
		}
		name := fmt.Sprintf("variation %d", x.Variation)
		if x.Variation >= 0 && x.Variation < len(keys) {
			name = keys[x.Variation]
		}
		s += fmt.Sprintf("%s %s%%", name, trimPct(float64(x.Weight)/1000))
	}
	return s
}

func trimPct(f float64) string { return fmt.Sprintf("%g", math.Round(f*1000)/1000) }

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
