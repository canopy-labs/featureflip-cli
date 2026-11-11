package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canopy-labs/featureflip-cli/internal/config"
)

// A config file nobody can parse, as a stray hand edit leaves it.
func corruptConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := config.Path(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

var errNoHome = errors.New("neither $XDG_CONFIG_HOME nor $HOME are defined")

func whoamiAPI(t *testing.T) *fakeAPI {
	api := newFakeAPI(t)
	api.on("GET", "/api/v1/me", 200, `{"type":"user","id":"u1","name":"Jo Dev","email":"jo@example.com"}`)
	api.on("GET", "/api/v1/orgs", 200, oneOrg)
	return api
}

func TestCorruptConfigIsSkippedWhenEnvHasToken(t *testing.T) {
	out, errOut, code := run(t, runOpts{API: whoamiAPI(t), ConfigDir: corruptConfig(t)}, "whoami")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "Name: Jo Dev") {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "Warning:") || !strings.Contains(errOut, "config.json") || !strings.Contains(errOut, "ignoring") {
		t.Errorf("want a warning naming the ignored file, stderr = %q", errOut)
	}
}

func TestCorruptConfigWarningKeepsJSONStdoutClean(t *testing.T) {
	out, _, code := run(t, runOpts{API: whoamiAPI(t), ConfigDir: corruptConfig(t)}, "whoami", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") || strings.Contains(out, "Warning") {
		t.Errorf("stdout must be only the JSON document: %q", out)
	}
}

func TestCorruptConfigWithoutEnvTokenIsNoToken(t *testing.T) {
	_, errOut, code := run(t, runOpts{API: whoamiAPI(t), ConfigDir: corruptConfig(t), NoToken: true}, "whoami")
	if code != 3 {
		t.Fatalf("exit %d, want 3 (no token); stderr %q", code, errOut)
	}
}

func TestNoConfigDirRunsFromEnv(t *testing.T) {
	out, errOut, code := run(t, runOpts{API: whoamiAPI(t), ConfigDirErr: errNoHome}, "whoami")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "Name: Jo Dev") {
		t.Errorf("stdout = %q", out)
	}
	if strings.Contains(errOut, "Warning") {
		t.Errorf("a missing config dir means nothing is saved, not a warning: %q", errOut)
	}
}

func TestNoConfigDirWithoutEnvTokenIsNoToken(t *testing.T) {
	_, _, code := run(t, runOpts{API: whoamiAPI(t), ConfigDirErr: errNoHome, NoToken: true}, "whoami")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
}

// login writes the file, so it must not silently replace one it can't read
// (that would drop the saved org/project defaults the user may want back).
func TestLoginRefusesCorruptConfig(t *testing.T) {
	dir := corruptConfig(t)
	_, errOut, code := run(t, runOpts{API: whoamiAPI(t), ConfigDir: dir, NoToken: true, Stdin: "ffp_new\n"}, "login")
	if code == 0 {
		t.Fatal("login overwrote an unreadable config file")
	}
	if !strings.Contains(errOut, "config.json") {
		t.Errorf("stderr should name the file: %q", errOut)
	}
	raw, _ := os.ReadFile(config.Path(dir))
	if string(raw) != "{" {
		t.Errorf("config file changed to %q", raw)
	}
}

func TestLoginWithoutConfigDirFails(t *testing.T) {
	_, _, code := run(t, runOpts{API: whoamiAPI(t), ConfigDirErr: errNoHome, NoToken: true, Stdin: "ffp_new\n"}, "login")
	if code == 0 {
		t.Fatal("login has nowhere to save the token and must fail")
	}
}

func TestLogoutWithoutConfigDirSucceeds(t *testing.T) {
	out, _, code := run(t, runOpts{ConfigDirErr: errNoHome, NoToken: true}, "logout")
	if code != 0 || !strings.Contains(out, "Logged out.") {
		t.Fatalf("exit %d out %q", code, out)
	}
}

// A URL with no scheme is a usage mistake (exit 2), caught before any request.
func TestSchemelessAPIURLIsUsageError(t *testing.T) {
	api := whoamiAPI(t)
	_, errOut, code := run(t, runOpts{API: api, Env: map[string]string{"FEATUREFLIP_API_URL": "localhost:5000"}}, "whoami")
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr %q", code, errOut)
	}
	if !strings.Contains(errOut, "https://") || strings.Contains(errOut, "unsupported protocol scheme") {
		t.Errorf("stderr = %q", errOut)
	}
}
