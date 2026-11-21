package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
)

func TestEnrollmentApprovalClaimAndCredentialIsolation(t *testing.T) {
	issued := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/example/actions/runner-groups":
			fmt.Fprint(w, `{"runner_groups":[{"id":1,"name":"apple-builds","visibility":"selected"},{"id":2,"name":"windows-builds","visibility":"selected"}]}`)
		case "/orgs/example/actions/runners":
			fmt.Fprint(w, `{"total_count":0,"runners":[]}`)
		case "/orgs/example/actions/runners/downloads":
			fmt.Fprintf(w, `[{"os":"osx","architecture":"arm64","download_url":"https://github.com/actions/runner/releases/download/v0/test.tar.gz","sha256_checksum":"%s"},{"os":"win","architecture":"x64","download_url":"https://github.com/actions/runner/releases/download/v0/test.zip","sha256_checksum":"%s"}]`, strings.Repeat("a", 64), strings.Repeat("b", 64))
		case "/orgs/example/actions/runners/registration-token":
			issued++
			w.WriteHeader(201)
			fmt.Fprint(w, `{"token":"short-lived-synthetic-registration"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	github := fleet.NewGitHub(func() string { return "synthetic-server-credential" })
	github.Base = api.URL
	cfg := Config{AllowedHosts: []string{"localhost"}, Provisioning: Provisioning{Enabled: true, Profiles: []Profile{
		{ID: "apple", Organization: "example", Group: "apple-builds", Labels: []string{"apple-build"}},
		{ID: "windows", OS: "win", Organization: "example", Group: "windows-builds", Labels: []string{"windows-build"}},
	}}}
	if err := cfg.Provisioning.validate(); err != nil {
		t.Fatal(err)
	}
	h := Handler(fleet.New(), cfg, github)
	request := func(method, path string, data any, secret string) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if data != nil {
			json.NewEncoder(&b).Encode(data)
		}
		req := httptest.NewRequest(method, path, &b)
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	secret := strings.Repeat("b", 64)
	w := request("POST", "/api/enrollment", map[string]string{"name": "new-mac", "architecture": "arm64", "developer_dir": "/Applications/Xcode.app/Contents/Developer", "secret": secret}, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	path := "/api/enrollment/" + result.ID
	if w = request("POST", path+"/claim", nil, strings.Repeat("c", 64)); w.Code != 404 {
		t.Fatal("another machine could claim")
	}
	w = request("POST", path+"/claim", nil, secret)
	if issued != 0 || !strings.Contains(w.Body.String(), "pending") {
		t.Fatal("issued before approval")
	}
	w = request("POST", path+"/approve", map[string]string{"profile": "unconfigured", "name": "new-mac"}, "")
	if w.Code != 400 {
		t.Fatal("unconfigured org/profile accepted")
	}
	w = request("POST", path+"/approve", map[string]string{"profile": "apple", "name": "new-mac"}, "")
	if w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	w = request("POST", path+"/claim", nil, secret)
	if w.Code != 200 || issued != 1 || !strings.Contains(w.Body.String(), "short-lived-synthetic-registration") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/enrollment", nil, "")
	if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "short-lived-synthetic-registration") || strings.Contains(w.Body.String(), "synthetic-server-credential") {
		t.Fatal("credential exposed to operator list")
	}
	w = request("POST", path+"/claim", nil, secret)
	if issued != 1 || strings.Contains(w.Body.String(), "registration_token") {
		t.Fatal("credential claimed more than once")
	}
	windows := map[string]string{"name": "new-windows", "os": "win", "architecture": "arm64", "secret": secret}
	if w = request("POST", "/api/enrollment", windows, ""); w.Code != 400 {
		t.Fatal("Windows ARM64 accepted")
	}
	windows["architecture"] = "x64"
	w = request("POST", "/api/enrollment", windows, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	path = "/api/enrollment/" + result.ID
	if w = request("POST", path+"/approve", map[string]string{"profile": "apple", "name": "new-windows"}, ""); w.Code != 400 || issued != 1 {
		t.Fatal("cross-platform approval accepted")
	}
	if w = request("POST", path+"/approve", map[string]string{"profile": "windows", "name": "new-windows"}, ""); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("POST", path+"/claim", nil, secret); w.Code != 200 || issued != 2 || !strings.Contains(w.Body.String(), "test.zip") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestProvisioningIsOptIn(t *testing.T) {
	h := Handler(fleet.New(), Config{AllowedHosts: []string{"localhost"}})
	req := httptest.NewRequest("GET", "/api/enrollment", nil)
	req.Host = "localhost"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Body.String())
	}
	c := Provisioning{Enabled: true, Profiles: []Profile{{ID: "example", Organization: "example", Group: "group", Labels: []string{"ARM64", "apple-build"}}}}
	if c.validate() == nil {
		t.Fatal("profile can spoof host architecture")
	}
}

func TestEnrollmentExpiry(t *testing.T) {
	now := time.Now()
	e := enrollments{entries: map[string]*Enrollment{
		"expired": {Created: now.Add(-11 * time.Minute), Status: "approved"},
		"pending": {Created: now.Add(-time.Minute), Status: "pending"},
	}, now: func() time.Time { return now }}
	e.prune()
	if len(e.entries) != 1 || e.entries["pending"] == nil {
		t.Fatal("expired approval retained or live request lost")
	}
}

func TestEnrollmentRejectsTrailingJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/enrollment", strings.NewReader(`{"name":"valid"} {"name":"injected"}`))
	req.Header.Set("Content-Type", "application/json")
	var in struct {
		Name string `json:"name"`
	}
	if decodeBody(req, &in) == nil {
		t.Fatal("accepted multiple request objects")
	}
}
