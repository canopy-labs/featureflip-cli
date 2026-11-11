package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/canopy-labs/featureflip-cli/internal/api"
)

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// table prints rows under upper-case headers in aligned columns.
func table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	upper := make([]string, len(headers))
	for i, h := range headers {
		upper[i] = strings.ToUpper(h)
	}
	fmt.Fprintln(tw, strings.Join(upper, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// useColor reports whether ON/OFF may be colored: stdout (where the color
// goes) is a terminal, and neither --no-color nor NO_COLOR asked for plain text.
func useColor(env Env, g *globals) bool {
	return !g.noColor && env.Getenv("NO_COLOR") == "" && env.StdoutTTY
}

// onOff renders an enabled state, green/dim on a color terminal.
func onOff(env Env, g *globals, enabled bool) string {
	if !useColor(env, g) {
		if enabled {
			return "on"
		}
		return "off"
	}
	if enabled {
		return "\x1b[32mon\x1b[0m"
	}
	return "\x1b[2moff\x1b[0m"
}

const notFoundHint = "This can also mean the public API isn't enabled for the organization, " +
	"or a service token isn't scoped to this project."

// renderError writes a failed run's error: a JSON document on stdout with
// --json (so a script reads one stream), otherwise lines on stderr.
func renderError(env Env, g *globals, err error) {
	var apiErr *api.Error
	isAPI := errors.As(err, &apiErr)

	if g.json {
		doc := map[string]any{"error": errorCode(err), "message": err.Error(), "status": nil}
		if isAPI {
			doc["status"] = apiErr.Status
			if apiErr.DocsURL != "" {
				doc["docs_url"] = apiErr.DocsURL
			}
			if len(apiErr.Fields) > 0 {
				doc["fields"] = apiErr.Fields
			}
			if len(apiErr.DidYouMean) > 0 {
				doc["did_you_mean"] = apiErr.DidYouMean
			}
			if apiErr.RetryAfter != nil {
				doc["retry_after"] = *apiErr.RetryAfter
			}
			if len(apiErr.PlanLimit) > 0 && string(apiErr.PlanLimit) != "null" {
				doc["plan_limit"] = apiErr.PlanLimit
			}
		}
		printJSON(env.Out, doc)
		return
	}

	fmt.Fprintf(env.Err, "Error: %s\n", err)
	if !isAPI {
		return
	}
	fields := make([]string, 0, len(apiErr.Fields))
	for f := range apiErr.Fields {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		fmt.Fprintf(env.Err, "  %s: %s\n", f, strings.Join(apiErr.Fields[f], "; "))
	}
	if len(apiErr.DidYouMean) > 0 {
		fmt.Fprintf(env.Err, "Did you mean: %s\n", strings.Join(apiErr.DidYouMean, ", "))
	}
	if apiErr.Status == 404 {
		fmt.Fprintln(env.Err, notFoundHint)
	}
}

// errorCode is the machine-readable "error" value for --json output.
func errorCode(err error) string {
	var apiErr *api.Error
	switch {
	case errors.As(err, &apiErr):
		return apiErr.Code
	case errors.Is(err, api.ErrNoToken):
		return "no_token"
	case errors.As(err, new(UsageError)):
		return "usage"
	case errors.As(err, new(ConfirmError)):
		return "confirmation_required"
	case errors.As(err, new(authError)):
		return "unauthorized"
	case errors.As(err, new(driftError)):
		return "drift"
	}
	return "unexpected"
}
