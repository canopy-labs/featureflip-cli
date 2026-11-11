package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
	"github.com/canopy-labs/featureflip-cli/internal/typegen"
)

// driftError means generate types --check found the file out of date. Exit 7.
type driftError struct{ msg string }

func (e driftError) Error() string { return e.msg }
func (driftError) ExitCode() int   { return exitcode.Drift }

// detailConcurrency bounds the per-flag requests generate types makes.
const detailConcurrency = 4

func newGenerateCmd(env Env, g *globals) *cobra.Command {
	gen := groupCmd("generate", "Generate code from the project's flags")
	var out string
	var check bool
	types := &cobra.Command{
		Use:   "types",
		Short: "Generate TypeScript types for the project's flag keys and values",
		Long: "Writes a TypeScript module with the project's flag keys and each flag's value type.\n" +
			"With --check, compares against --out instead and exits 7 if it is out of date.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if check && out == "" {
				return UsageError{"--check needs --out <file> to compare against"}
			}
			s, err := newSession(env, g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			org, project, err := s.Scope(ctx)
			if err != nil {
				return err
			}
			specs, err := flagSpecs(ctx, s, org, project)
			if err != nil {
				return err
			}
			src, warnings := typegen.Generate(project, specs)
			for _, w := range warnings {
				fmt.Fprintf(env.Err, "Warning: %s\n", w)
			}

			result := map[string]any{"project": project, "flags": len(specs), "out": nil}
			switch {
			case check:
				existing, err := os.ReadFile(out)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				// Compare with CRLF folded to LF: a Windows checkout
				// (core.autocrlf) holds the same file with CRLF endings.
				if !bytes.Equal(bytes.ReplaceAll(existing, []byte("\r\n"), []byte("\n")), src) {
					return driftError{fmt.Sprintf("%s is out of date; run featureflip generate types --out %s", out, out)}
				}
				result["out"], result["upToDate"] = out, true
			case out != "":
				if err := os.WriteFile(out, src, 0o644); err != nil {
					return err
				}
				result["out"], result["written"] = out, true
				if !g.json {
					fmt.Fprintf(env.Err, "Wrote %d flags to %s\n", len(specs), out)
				}
			default:
				if !g.json {
					_, err = env.Out.Write(src)
					return err
				}
				result["source"] = string(src)
			}
			if g.json {
				return printJSON(env.Out, result)
			}
			return nil
		},
	}
	types.Flags().StringVar(&out, "out", "", "file to write (default: stdout)")
	types.Flags().BoolVar(&check, "check", false, "don't write; exit 7 if --out is out of date")
	gen.AddCommand(types)
	return gen
}

// flagSpecs lists the project's active flags and, for String flags only,
// fetches their variations (the list route doesn't carry them).
func flagSpecs(ctx context.Context, s *session, org, project string) ([]typegen.FlagSpec, error) {
	// The API includes archived flags unless asked for active ones.
	q := url.Values{"archived": {"false"}}
	flags, err := api.ListAll[api.FlagListItem](ctx, s.API, projectPath(org, project, "flags"), q)
	if err != nil {
		return nil, err
	}
	specs := make([]typegen.FlagSpec, len(flags))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		slots    = make(chan struct{}, detailConcurrency)
	)
	for i, f := range flags {
		specs[i] = typegen.FlagSpec{Key: f.Key, Type: f.Type}
		if f.Type != "String" {
			continue
		}
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			var flag api.Flag
			err := s.API.Do(ctx, "GET", projectPath(org, project, "flags", key), api.Request{}, &flag)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			for _, v := range flag.Variations {
				specs[i].Values = append(specs[i].Values, v.Value)
			}
		}(i, f.Key)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return specs, nil
}
