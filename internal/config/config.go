// Package config reads and writes the CLI's saved settings and resolves them
// against the environment.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultBaseURL = "https://api.featureflip.io"

// File is what `featureflip login` saves. The token is a secret: the file is
// written owner-only.
type File struct {
	Token   string `json:"token,omitempty"`
	Org     string `json:"org,omitempty"`
	Project string `json:"project,omitempty"`
	APIURL  string `json:"apiUrl,omitempty"`
}

// Path is the config file under the user's config root (os.UserConfigDir).
func Path(root string) string {
	return filepath.Join(root, "featureflip", "config.json")
}

// Load reads the config file. A missing file is an empty config, not an error.
// The warnings are for the user (printed to stderr), not failures.
func Load(root string) (File, []string, error) {
	path := Path(root)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil, nil
	}
	if err != nil {
		return File{}, nil, err
	}
	var warnings []string
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
			warnings = append(warnings, fmt.Sprintf(
				"config file %s is readable by other users; run chmod 600 %s", path, path))
		}
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, warnings, fmt.Errorf("reading %s: %w", path, err)
	}
	return f, warnings, nil
}

// Save writes the config file owner-only, replacing it atomically.
func Save(root string, f File) error {
	path := Path(root)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil && runtime.GOOS != "windows" {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Settings is the effective configuration for one run.
type Settings struct {
	Token, BaseURL, Org, Project string
}

// Resolve layers the environment over the config file. Each environment
// variable wins when it holds anything but whitespace: an empty CI secret
// renders as "" or " ", and must not shadow a saved value.
func Resolve(getenv func(string) string, f File) (Settings, []string) {
	pick := func(name, fallback string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return strings.TrimSpace(fallback)
	}
	s := Settings{
		Token:   pick("FEATUREFLIP_TOKEN", f.Token),
		BaseURL: strings.TrimRight(pick("FEATUREFLIP_API_URL", f.APIURL), "/"),
		Org:     pick("FEATUREFLIP_ORG", f.Org),
		Project: pick("FEATUREFLIP_PROJECT", f.Project),
	}
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
	var warnings []string
	if u, err := url.Parse(s.BaseURL); err == nil && strings.EqualFold(u.Scheme, "http") && !isLoopback(u.Hostname()) {
		warnings = append(warnings,
			"FEATUREFLIP_API_URL uses plain http to a non-local host; your token will travel unencrypted")
	}
	return s, warnings
}

// CheckBaseURL refuses an API URL the HTTP client can't use: one without an
// http or https scheme and a host (`localhost:5000`, `api.example.com`).
func CheckBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (!strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http")) || u.Host == "" {
		return fmt.Errorf("the API URL %q needs a scheme and host, e.g. https://api.featureflip.io", raw)
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
