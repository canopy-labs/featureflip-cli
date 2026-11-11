package api

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestListAllFollowsCursor(t *testing.T) {
	var queries []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("cursor") == "" {
			w.Write([]byte(`{"items":[{"key":"a"},{"key":"b"}],"next_cursor":"c2"}`))
			return
		}
		w.Write([]byte(`{"items":[{"key":"c"}],"next_cursor":null}`))
	})
	type item struct {
		Key string `json:"key"`
	}
	got, err := ListAll[item](context.Background(), c, P("orgs", "acme", "projects"), map[string][]string{"search": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []item{{"a"}, {"b"}, {"c"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("items = %v", got)
	}
	if want := []string{"limit=100&search=x", "cursor=c2&limit=100&search=x"}; !reflect.DeepEqual(queries, want) {
		t.Errorf("queries = %v, want %v", queries, want)
	}
}
