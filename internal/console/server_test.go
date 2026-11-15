package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPValidation(t *testing.T) {
	state := fleet.New()
	handler := Handler(state, Config{AllowedHosts: []string{"localhost"}})
	cases := []struct {
		method, path, body, host, origin string
		code                             int
	}{{"GET", "/api/state", "", "localhost", "", 200}, {"GET", "/api/state", "", "evil.example", "", 403}, {"GET", "/api/state", "", "localhost", "http://evil.example", 403}, {"POST", "/api/telemetry", `{"schema":1,"id":"host","name":"Host","cores":4}`, "localhost", "", 204}, {"POST", "/api/telemetry", `{"schema":1,"id":"host","name":"Host","cores":4,"secret":"no"}`, "localhost", "", 400}, {"POST", "/api/telemetry", strings.Repeat("x", 70000), "localhost", "", 400}, {"POST", "/api/control", "{}", "localhost", "", 405}}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != c.code {
			t.Errorf("%s %s: got %d want %d", c.method, c.path, w.Code, c.code)
		}
	}
}
func TestMCPRealClient(t *testing.T) {
	state := fleet.New()
	server := httptest.NewServer(Handler(state, Config{AllowedHosts: []string{"127.0.0.1"}}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatal(tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_hosts", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_host_history", Arguments: map[string]any{"id": "unknown", "minutes": 500}})
	if err != nil || !result.IsError {
		t.Fatal("invalid history accepted", result, err)
	}
}
func TestStaticAssets(t *testing.T) {
	server := httptest.NewServer(Handler(fleet.New(), Config{AllowedHosts: []string{"127.0.0.1"}}))
	defer server.Close()
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		r, e := http.Get(server.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal(path, r.Status)
		}
	}
}
