package fleet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJobsAttemptAndRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/example/app/actions/runs":
			if r.URL.Query().Get("status") == "queued" {
				fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
				return
			}
			fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":10,"run_attempt":2}]}`)
		case "/repos/example/app/actions/runs/10/attempts/2/jobs":
			fmt.Fprint(w, `{"total_count":2,"jobs":[{"id":20,"name":"Build","status":"in_progress","runner_id":7,"runner_name":"mac","html_url":"https://github.com/example/app/actions/runs/10","started_at":"2026-01-01T00:00:00Z"},{"id":21,"name":"Waiting","status":"queued","runner_id":0}]}`)
		default:
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
		}
	}))
	defer server.Close()
	g := NewGitHub(func() string { return "" })
	g.Base = server.URL
	jobs, err := g.Jobs(context.Background(), "example/app")
	if err != nil || len(jobs) != 2 || jobs[0].Attempt != 2 || jobs[1].RunnerID != 0 {
		t.Fatal(jobs, err)
	}
	if _, err = g.Runners(context.Background(), "orgs/example"); err == nil || time.Until(g.RetryAfter) < time.Minute {
		t.Fatal("rate limit not respected", err)
	}
}

func TestFreshnessIdentityAndBounds(t *testing.T) {
	s := New()
	now := time.Now()
	s.now = func() time.Time { return now }
	cpu := 30.0
	r := Report{Schema: 1, ID: "host-1", Name: "Example", OS: "darwin", Arch: "arm64", Cores: 8, CPU: &cpu, Runners: []RunnerRef{{Scope: "orgs/example", ID: 1}}}
	for i := 0; i < 600; i++ {
		if err := s.Ingest(r); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := s.History(r.ID, 120)
	if len(p) != 480 {
		t.Fatal(len(p))
	}
	s.Update("orgs/example", []Runner{{Scope: "orgs/example", ID: 1}, {Scope: "orgs/other", ID: 1}}, nil, nil)
	v := s.Snapshot()
	for _, runner := range v.Runners {
		if runner.Scope == "orgs/example" && runner.HostID != "host-1" {
			t.Fatal("missing exact join")
		}
		if runner.Scope == "orgs/other" && runner.HostID != "" {
			t.Fatal("cross-scope join")
		}
	}
	now = now.Add(46 * time.Second)
	if !s.Snapshot().Hosts[0].Stale {
		t.Fatal("heartbeat did not expire")
	}
	if _, e := s.History(r.ID, 121); e == nil {
		t.Fatal("unbounded history")
	}
	r.ID = "../bad"
	if s.Ingest(r) == nil {
		t.Fatal("invalid ID accepted")
	}
}
func TestGitHubPaginationCacheAndFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer synthetic-test-value" {
			t.Error("missing authorization")
		}
		if r.Header.Get("If-None-Match") == "v1" {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", "v1")
		_, _ = w.Write([]byte(`{"total_count":1,"runners":[{"id":1,"name":"sample","os":"osx","status":"online","busy":true,"labels":[{"name":"ARM64"}]}]}`))
	}))
	defer server.Close()
	g := NewGitHub(func() string { return "synthetic-test-value" })
	g.Base = server.URL
	for i := 0; i < 2; i++ {
		r, e := g.Runners(context.Background(), "orgs/example")
		if e != nil || len(r) != 1 || !r[0].Busy {
			t.Fatal(r, e)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	s := New()
	r, _ := g.Runners(context.Background(), "orgs/example")
	s.Update("orgs/example", r, nil, nil)
	s.Update("orgs/example", nil, nil, context.DeadlineExceeded)
	v := s.Snapshot()
	if len(v.Runners) != 1 || !v.Sources["orgs/example"].Stale {
		t.Fatal("last-known data lost")
	}
}
func TestInvalidAndExcessHosts(t *testing.T) {
	s := New()
	s.maxHosts = 1
	r := Report{Schema: 1, ID: "one", Name: "One", Cores: 4}
	if e := s.Ingest(r); e != nil {
		t.Fatal(e)
	}
	r.ID = "two"
	if s.Ingest(r) == nil {
		t.Fatal("host cap ignored")
	}
	r.ID = "one"
	cpu := 101.0
	r.CPU = &cpu
	if s.Ingest(r) == nil {
		t.Fatal("invalid CPU")
	}
}

func TestPoolAvailability(t *testing.T) {
	now := time.Now()
	ac := true
	r := Runner{Status: "online", Labels: []string{"orchard-managed", "apple-build"}}
	h := Host{Report: Report{Power: &Power{AC: &ac}}, Received: now}
	source := Source{Updated: &now}
	for _, tc := range []struct {
		plugged, busy bool
		want          string
	}{{true, false, "ready"}, {true, true, "busy"}, {false, true, "finishing-on-battery"}, {false, false, "battery"}} {
		ac = tc.plugged
		r.Busy = tc.busy
		if got := availability(r, h, source, now); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
	if got := availability(r, h, source, now.Add(65*time.Second)); got != "unknown" {
		t.Fatal(got)
	}
}
