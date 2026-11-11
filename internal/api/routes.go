package api

// Route is one public API operation the CLI calls, spelled as the OpenAPI
// document spells it. contract_test.go checks each against the document, so
// a renamed route or parameter fails this package's tests rather than users.
type Route struct {
	Method   string
	Template string
	Query    []string
	Headers  []string
}

const (
	project = "/api/v1/orgs/{org}/projects/{project}"
	flag    = project + "/flags/{flag}"
)

var Routes = []Route{
	{Method: "GET", Template: "/api/v1/me"},
	{Method: "GET", Template: "/api/v1/orgs", Query: []string{"limit", "cursor"}},
	{Method: "GET", Template: "/api/v1/orgs/{org}/projects", Query: []string{"limit", "cursor"}},
	{Method: "GET", Template: project + "/environments", Query: []string{"limit", "cursor"}},
	{Method: "GET", Template: project + "/environments/{env}"},
	{Method: "GET", Template: project + "/segments", Query: []string{"limit", "cursor"}},
	{Method: "GET", Template: project + "/compare", Query: []string{"base", "other", "limit", "cursor"}},
	{Method: "GET", Template: project + "/flags", Query: []string{"search", "tag", "archived", "limit", "cursor"}},
	{Method: "POST", Template: project + "/flags"},
	{Method: "GET", Template: flag},
	{Method: "GET", Template: flag + "/environments"},
	{Method: "GET", Template: flag + "/environments/{env}"},
	{Method: "POST", Template: flag + "/environments/{env}/toggle", Headers: []string{"X-Intent-Reason"}},
	{Method: "POST", Template: flag + "/archive", Query: []string{"force"}, Headers: []string{"X-Intent-Reason"}},
	{Method: "POST", Template: flag + "/restore", Headers: []string{"X-Intent-Reason"}},
}
