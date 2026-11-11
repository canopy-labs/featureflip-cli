package cmd

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	out, _, code := run(t, runOpts{}, "version")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"featureflip dev", "go1.", runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestVersionJSON(t *testing.T) {
	out, _, code := run(t, runOpts{}, "version", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"version", "commit", "go", "platform"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q in %v", k, got)
		}
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	_, errOut, code := run(t, runOpts{}, "nope")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.HasPrefix(errOut, "Error:") {
		t.Errorf("stderr %q should start with Error:", errOut)
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	_, _, code := run(t, runOpts{}, "version", "--bogus")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestUnknownSubcommandIsUsageError(t *testing.T) {
	for _, group := range []string{"flags", "projects", "environments", "segments"} {
		_, errOut, code := run(t, runOpts{}, group, "bogus")
		if code != 2 || !strings.Contains(errOut, `unknown command "bogus"`) {
			t.Errorf("%s bogus: exit %d stderr %q", group, code, errOut)
		}
	}
}

func TestBareGroupPrintsHelp(t *testing.T) {
	out, _, code := run(t, runOpts{}, "flags")
	if code != 0 || !strings.Contains(out, "Available Commands") {
		t.Errorf("exit %d stdout %q", code, out)
	}
}

// An unknown flag before the command name must be reported as itself, not as
// cobra's guess that the next word was its value ("unknown command list").
func TestUnknownFlagBeforeCommandNamesTheFlag(t *testing.T) {
	_, errOut, code := run(t, runOpts{}, "--bogus", "flags", "list")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--bogus") || strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr = %q, want it to name --bogus", errOut)
	}
}

// --json is honored even when parsing failed before reaching it.
func TestParseErrorBeforeJSONFlagStillPrintsJSON(t *testing.T) {
	for _, args := range [][]string{
		{"--bogus", "flags", "list", "--json"},
		{"--json=true", "--bogus", "flags", "list"},
	} {
		out, errOut, code := run(t, runOpts{}, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Errorf("%v: stdout is not one JSON document: %q (stderr %q)", args, out, errOut)
			continue
		}
		if doc["error"] != "usage" || !strings.Contains(doc["message"].(string), "--bogus") {
			t.Errorf("%v: doc = %v", args, doc)
		}
		if errOut != "" {
			t.Errorf("%v: stderr = %q, want nothing", args, errOut)
		}
	}
}

// A --json after the "--" terminator is an argument, not the flag.
func TestJSONAfterTerminatorIsNotTheFlag(t *testing.T) {
	out, _, code := run(t, runOpts{}, "--bogus", "--", "--json")
	if code != 2 || strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("exit %d stdout %q", code, out)
	}
}

func TestUnknownLeadingFlag(t *testing.T) {
	root := newRoot(Env{}, &globals{})
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--bogus", "flags", "list"}, "--bogus"},
		{[]string{"--bogus=1", "flags", "list"}, "--bogus"},
		{[]string{"-x", "flags", "list"}, "-x"},
		{[]string{"--org", "acme", "--bogus", "flags"}, "--bogus"},
		{[]string{"--org", "--bogus", "flags"}, ""}, // --bogus is --org's value
		{[]string{"--org=acme", "--json", "flags", "list"}, ""},
		{[]string{"--json", "--yes", "flags"}, ""},
		{[]string{"--help"}, ""},
		{[]string{"-h", "flags"}, ""},
		{[]string{"flags", "--env", "dev", "off", "x"}, ""}, // subcommand flags are cobra's
		{[]string{"flags", "--bogus"}, ""},
		{[]string{"--", "--bogus"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := unknownLeadingFlag(root, c.args); got != c.want {
			t.Errorf("%v: got %q, want %q", c.args, got, c.want)
		}
	}
}

func TestWantsJSON(t *testing.T) {
	cases := map[string]bool{
		"--bogus flags list --json":  true,
		"--json=true --bogus":        true,
		"--json=false --bogus":       false,
		"--json --json=false":        false,
		"--bogus -- --json":          false,
		"--bogus flags list --jsonx": false,
	}
	for in, want := range cases {
		if got := wantsJSON(strings.Fields(in)); got != want {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
}
