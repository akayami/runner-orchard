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
	bindings := []WindowsWorker{{ID: "laptop", Organization: "example", RunnerID: 42, TokenHash: hex.EncodeToString(hash[:])}}
	if err := validateWindowsWorkers(bindings); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerWindowsWorkers(mux, bindings, github)
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
	if w.Code != 200 || w.Body.String() != "{\"eligible\":false,\"busy\":true,\"online\":true}\n" || requests.Load() != 2 {
		t.Fatal(w.Code, w.Body.String(), requests.Load())
	}
}
