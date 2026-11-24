package console

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/akayami/runner-orchard/internal/fleet"
)

func TestWindowsWorkerControlIsBoundToOneRunner(t *testing.T) {
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case r.Method == "GET" && r.URL.Path == "/orgs/example/actions/runners/42":
			fmt.Fprint(w, `{"os":"windows","status":"online","busy":true,"labels":[{"name":"windows-build"},{"name":"other"}]}`)
		case r.Method == "DELETE" && r.URL.Path == "/orgs/example/actions/runners/42/labels/windows-build":
			fmt.Fprint(w, `{"labels":[{"name":"other"}]}`)
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	github := fleet.NewGitHub(func() string { return "test" })
	github.Base = api.URL
	token := strings.Repeat("ab", 32)
	raw, _ := hex.DecodeString(token)
	hash := sha256.Sum256(raw)
	bindings := []WorkerBinding{{ID: "laptop", Organization: "example", RunnerID: 42, TokenHash: hex.EncodeToString(hash[:])}}
	if err := validateWorkerBindings(bindings); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerWorkerControl(mux, bindings, github, "windows")
	request := func(id, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/windows-workers/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	if w := request("other", "Bearer "+token, `{"eligible":false}`); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("laptop", token, `{"eligible":false}`); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("laptop", "Bearer "+token, `{"eligible":false,"runner_id":99}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if requests.Load() != 0 {
		t.Fatal("invalid request reached GitHub")
	}
	w := request("laptop", "Bearer "+token, `{"eligible":false}`)
	if w.Code != 200 || w.Body.String() != "{\"eligible\":false,\"busy\":true,\"online\":true,\"runner_id\":42}\n" || requests.Load() != 2 {
		t.Fatal(w.Code, w.Body.String(), requests.Load())
	}
}

func TestMacPowerControlCannotRemoveRegistration(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.Method == "GET" && r.URL.Path == "/orgs/example/actions/runners/12":
			fmt.Fprint(w, `{"os":"macOS","busy":true,"status":"online","labels":[{"name":"apple-build"},{"name":"macos-signing"}]}`)
		case r.Method == "DELETE" && r.URL.Path == "/orgs/example/actions/runners/12/labels/apple-build":
			fmt.Fprint(w, `{"labels":[{"name":"macos-signing"}]}`)
		default:
			t.Errorf("unexpected operation %s %s", r.Method, r.URL.Path)
		}
	}))
	defer api.Close()
	gh := fleet.NewGitHub(func() string { return "test" })
	gh.Base = api.URL
	raw := make([]byte, 32)
	hash := sha256.Sum256(raw)
	mux := http.NewServeMux()
	registerWorkerControl(mux, []WorkerBinding{{ID: "mac", Organization: "example", RunnerID: 12, TokenHash: hex.EncodeToString(hash[:])}}, gh, "mac")
	for _, tc := range []struct {
		body string
		code int
	}{{`{"remove":true}`, 400}, {`{"eligible":false}`, 200}} {
		req := httptest.NewRequest("POST", "/api/mac-workers/mac", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+hex.EncodeToString(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
