package console

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed web/*
var assets embed.FS

type Config struct {
	Listen       string       `json:"listen"`
	AllowedHosts []string     `json:"allowed_hosts"`
	Scopes       []string     `json:"scopes"`
	Repositories []string     `json:"repositories"`
	TokenFile    string       `json:"token_file"`
	PollSeconds  int          `json:"poll_seconds"`
	Provisioning Provisioning `json:"provisioning"`
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen must be host:port")
	}
	if len(c.AllowedHosts) == 0 {
		return fmt.Errorf("allowed_hosts is required")
	}
	if c.PollSeconds < 15 || c.PollSeconds > 3600 {
		return fmt.Errorf("poll_seconds must be 15–3600")
	}
	if len(c.Scopes) > 32 || len(c.Repositories) > 32 {
		return fmt.Errorf("scope limit exceeded")
	}
	for _, s := range c.Scopes {
		if !fleet.ValidScope(s) {
			return fmt.Errorf("invalid scope")
		}
	}
	for _, r := range c.Repositories {
		if !fleet.ValidScope("repos/" + r) {
			return fmt.Errorf("invalid repository")
		}
	}
	return c.Provisioning.validate()
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type idArgs struct {
	ID string `json:"id" jsonschema:"Host ID or runner identity in scope#id form"`
}
type historyArgs struct {
	ID      string `json:"id"`
	Minutes int    `json:"minutes" jsonschema:"History window, 1 through 120 minutes"`
}

func NewMCP(state *fleet.State) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "runner-orchard", Version: "0.1.0"}, nil)
	add := func(name, description string, fn func() any) {
		mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
			return nil, fn(), nil
		})
	}
	add("list_runners", "List GitHub runners, explicit host associations and source freshness.", func() any {
		v := state.Snapshot()
		return map[string]any{"runners": v.Runners, "sources": v.Sources, "time": v.Time}
	})
	add("list_hosts", "List host telemetry; stale values are last-known, not live.", func() any { return state.Snapshot().Hosts })
	add("list_active_jobs", "List queued and running jobs from configured repositories; unassigned jobs have no runner ID.", func() any { v := state.Snapshot(); return map[string]any{"jobs": v.Jobs, "sources": v.Sources} })
	add("get_fleet_health", "Show collection errors, source freshness and host counts. This is read-only.", func() any {
		v := state.Snapshot()
		return map[string]any{"sources": v.Sources, "hosts": len(v.Hosts), "runners": len(v.Runners), "time": v.Time}
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_host", Description: "Read one host by stable ID.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, args idArgs) (*mcp.CallToolResult, any, error) {
		for _, h := range state.Snapshot().Hosts {
			if h.ID == args.ID {
				return nil, h, nil
			}
		}
		return nil, nil, fmt.Errorf("host not found")
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_runner", Description: "Read one runner by scope#id, for example orgs/example#123.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, args idArgs) (*mcp.CallToolResult, any, error) {
		v := state.Snapshot()
		for _, r := range v.Runners {
			if fmt.Sprintf("%s#%d", r.Scope, r.ID) == args.ID {
				return nil, map[string]any{"runner": r, "sources": v.Sources}, nil
			}
		}
		return nil, nil, fmt.Errorf("runner not found")
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_host_history", Description: "Read bounded in-memory CPU, memory, swap and disk history. CPU is whole-host percent, bytes are bytes; history resets at server restart.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, args historyArgs) (*mcp.CallToolResult, any, error) {
		points, err := state.History(args.ID, args.Minutes)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"points": points, "persistent": false, "cpu_unit": "percent", "memory_unit": "bytes"}, nil
	})
	return server
}
func Handler(state *fleet.State, config Config, github ...*fleet.GitHub) http.Handler {
	mux := http.NewServeMux()
	var provisioner *fleet.GitHub
	if len(github) > 0 {
		provisioner = github[0]
	}
	registerEnrollment(mux, config, provisioner)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, state.Snapshot()) })
	mux.HandleFunc("GET /api/history/{id}", func(w http.ResponseWriter, r *http.Request) {
		minutes := 120
		if value := r.URL.Query().Get("minutes"); value != "" {
			var err error
			minutes, err = strconv.Atoi(value)
			if err != nil {
				http.Error(w, "invalid minutes", 400)
				return
			}
		}
		points, err := state.History(r.PathValue("id"), minutes)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		jsonResponse(w, map[string]any{"points": points, "persistent": false})
	})
	mux.HandleFunc("POST /api/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "application/json required", 415)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var report fleet.Report
		if err := dec.Decode(&report); err != nil {
			http.Error(w, "invalid report", 400)
			return
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			http.Error(w, "one report required", 400)
			return
		}
		if err := state.Ingest(report); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, h := range state.Snapshot().Hosts {
			up := 1
			if h.Stale {
				up = 0
			}
			fmt.Fprintf(w, "orchard_host_up{host=%q} %d\norchard_host_last_received_seconds{host=%q} %d\n", h.ID, up, h.ID, h.Received.Unix())
			if h.Stale {
				continue
			}
			if h.CPU != nil {
				fmt.Fprintf(w, "orchard_host_cpu_percent{host=%q} %g\n", h.ID, *h.CPU)
			}
			if h.CPUPeak != nil {
				fmt.Fprintf(w, "orchard_host_cpu_peak_percent{host=%q} %g\n", h.ID, *h.CPUPeak)
			}
			if h.Memory != nil {
				fmt.Fprintf(w, "orchard_host_memory_used_bytes{host=%q} %d\norchard_host_memory_total_bytes{host=%q} %d\n", h.ID, h.Memory.Used, h.ID, h.Memory.Total)
			}
			if h.Disk != nil {
				fmt.Fprintf(w, "orchard_host_disk_free_bytes{host=%q} %d\n", h.ID, h.Disk.Free)
			}
		}
	})
	mcpServer := NewMCP(state)
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		file := "index.html"
		mime := "text/html; charset=utf-8"
		switch r.URL.Path {
		case "/":
		case "/app.js":
			file = "app.js"
			mime = "application/javascript"
		case "/style.css":
			file = "style.css"
			mime = "text/css"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := assets.ReadFile("web/" + file)
		if err != nil {
			http.Error(w, "asset unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", mime)
		_, _ = w.Write(data)
	})
	// One bounded global token bucket avoids an unbounded per-client registry.
	var mu sync.Mutex
	tokens := 120.0
	last := time.Now()
	slots := make(chan struct{}, 32)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		allowed := false
		for _, h := range config.AllowedHosts {
			if strings.EqualFold(h, host) {
				allowed = true
			}
		}
		if !allowed {
			http.Error(w, "host not allowed", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "origin not allowed", 403)
				return
			}
		}
		if r.URL.Path != "/healthz" {
			mu.Lock()
			now := time.Now()
			tokens += now.Sub(last).Seconds() * 10
			if tokens > 120 {
				tokens = 120
			}
			last = now
			ok := tokens >= 1
			if ok {
				tokens--
			}
			mu.Unlock()
			if !ok {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "request limit", 429)
				return
			}
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "server busy", 503)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		mux.ServeHTTP(w, r)
	})
}
