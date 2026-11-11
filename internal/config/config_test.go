package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSaveWrites0600And0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := Save(dir, File{Token: "ffp_x", Org: "acme"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(Path(dir)))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}
	got, warnings, err := Load(dir)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	if got.Token != "ffp_x" || got.Org != "acme" {
		t.Errorf("round trip = %+v", got)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	got, warnings, err := Load(t.TempDir())
	if err != nil || len(warnings) != 0 || got != (File{}) {
		t.Fatalf("got %+v %v %v", got, warnings, err)
	}
}

func TestLoadWarnsOnWideMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	if err := Save(dir, File{Token: "ffp_x"}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(Path(dir), 0o644)
	_, warnings, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "readable by other users") || !strings.Contains(warnings[0], "chmod 600") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestEnvTokenBeatsFile(t *testing.T) {
	s, _ := Resolve(env(map[string]string{"FEATUREFLIP_TOKEN": "ffp_env"}), File{Token: "ffp_file"})
	if s.Token != "ffp_env" {
		t.Errorf("token = %q", s.Token)
	}
}

func TestWhitespaceEnvTokenFallsBackToFile(t *testing.T) {
	s, _ := Resolve(env(map[string]string{"FEATUREFLIP_TOKEN": "  \n", "FEATUREFLIP_ORG": " "}), File{Token: "ffp_file", Org: "acme"})
	if s.Token != "ffp_file" || s.Org != "acme" {
		t.Errorf("settings = %+v", s)
	}
}

func TestEnvValuesAreTrimmed(t *testing.T) {
	s, _ := Resolve(env(map[string]string{"FEATUREFLIP_TOKEN": " ffp_env\n", "FEATUREFLIP_PROJECT": " web "}), File{})
	if s.Token != "ffp_env" || s.Project != "web" {
		t.Errorf("settings = %+v", s)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	s, _ := Resolve(env(nil), File{})
	if s.BaseURL != "https://api.featureflip.io" {
		t.Errorf("base = %q", s.BaseURL)
	}
}

func TestTrailingSlashStripped(t *testing.T) {
	s, _ := Resolve(env(map[string]string{"FEATUREFLIP_API_URL": "https://api.dev.example/"}), File{})
	if s.BaseURL != "https://api.dev.example" {
		t.Errorf("base = %q", s.BaseURL)
	}
	s, _ = Resolve(env(nil), File{APIURL: "https://from-file.example//"})
	if s.BaseURL != "https://from-file.example" {
		t.Errorf("base = %q", s.BaseURL)
	}
}

func TestPlainHTTPRemoteWarns(t *testing.T) {
	_, warnings := Resolve(env(map[string]string{"FEATUREFLIP_API_URL": "http://api.example.com"}), File{})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "plain http") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestPlainHTTPLocalhostDoesNotWarn(t *testing.T) {
	for _, u := range []string{"http://localhost:5000", "http://127.0.0.1:5000", "http://[::1]:5000"} {
		if _, warnings := Resolve(env(map[string]string{"FEATUREFLIP_API_URL": u}), File{}); len(warnings) != 0 {
			t.Errorf("%s: warnings = %v", u, warnings)
		}
	}
}

// Host names are case-insensitive: LOCALHOST is still this machine.
func TestPlainHTTPLocalhostAnyCaseDoesNotWarn(t *testing.T) {
	for _, u := range []string{"http://LOCALHOST:5000", "http://LocalHost"} {
		if _, warnings := Resolve(env(map[string]string{"FEATUREFLIP_API_URL": u}), File{}); len(warnings) != 0 {
			t.Errorf("%s warned: %v", u, warnings)
		}
	}
}

func TestCheckBaseURL(t *testing.T) {
	for _, ok := range []string{"https://api.featureflip.io", "http://localhost:5000", "HTTPS://api.example.com/base"} {
		if err := CheckBaseURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"localhost:5000", "api.example.com", "ftp://api.example.com", "https://", "://x"} {
		err := CheckBaseURL(bad)
		if err == nil {
			t.Errorf("%s: accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "https://") {
			t.Errorf("%s: message %q should say the URL needs https://", bad, err)
		}
	}
}
