// Package version holds the build's identity. Both values are set at link
// time by the release build (-ldflags -X); a plain `go build` reports "dev".
package version

var (
	Version = "dev"
	Commit  = "none"
)

// UserAgent is sent on every API request so the server can tell CLI traffic
// and versions apart.
func UserAgent() string { return "featureflip-cli/" + Version }
