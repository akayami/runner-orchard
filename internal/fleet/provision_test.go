package fleet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProvisionRefusesUnsafeGroupsAndExistingNames(t *testing.T) {
	for _, scenario := range []struct{ name, visibility, public, runners string }{
		{"all-repositories", "all", "false", `[]`},
		{"public-enabled", "selected", "true", `[]`},
		{"existing-name", "selected", "false", `[{"id":1,"name":"mac-worker"}]`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/orgs/example/actions/runner-groups":
					fmt.Fprintf(w, `{"runner_groups":[{"name":"apple-builds","visibility":%q,"allows_public_repositories":%s}]}`, scenario.visibility, scenario.public)
				case "/orgs/example/actions/runners":
					fmt.Fprintf(w, `{"runners":%s}`, scenario.runners)
				default:
					t.Errorf("unexpected credential/package request: %s", r.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer api.Close()
			g := NewGitHub(func() string { return "synthetic-credential" })
			g.Base = api.URL
			if _, _, err := g.Provision(context.Background(), "example", "apple-builds", "mac-worker", "arm64"); err == nil {
				t.Fatal("unsafe registration allowed")
			}
		})
	}
}
