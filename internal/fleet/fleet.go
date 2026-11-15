package fleet

import (
	"errors"
	"math"
	"regexp"
	"sort"
	"sync"
	"time"
)

type RunnerRef struct {
	Scope string `json:"scope"`
	ID    int64  `json:"id"`
}
type Memory struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`
}
type Swap struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
	In    uint64 `json:"in"`
	Out   uint64 `json:"out"`
}
type Disk struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}
type Power struct {
	AC        *bool `json:"ac,omitempty"`
	LidClosed *bool `json:"lid_closed,omitempty"`
	Active    *bool `json:"active,omitempty"`
	Healthy   *bool `json:"healthy,omitempty"`
}
type Report struct {
	Schema         int         `json:"schema"`
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	OS             string      `json:"os"`
	Arch           string      `json:"arch"`
	Cores          int         `json:"cores"`
	CPU            *float64    `json:"cpu,omitempty"`
	CPUPeak        *float64    `json:"cpu_peak,omitempty"`
	Memory         *Memory     `json:"memory,omitempty"`
	Swap           *Swap       `json:"swap,omitempty"`
	Disk           *Disk       `json:"disk,omitempty"`
	Load           *float64    `json:"load,omitempty"`
	Uptime         uint64      `json:"uptime_seconds"`
	Power          *Power      `json:"power,omitempty"`
	Runners        []RunnerRef `json:"runners"`
	Drained        *bool       `json:"drained,omitempty"`
	ServiceHealthy *bool       `json:"service_healthy,omitempty"`
}
type Host struct {
	Report
	Received time.Time `json:"received"`
	Stale    bool      `json:"stale"`
}
type Point struct {
	Time   time.Time `json:"time"`
	CPU    *float64  `json:"cpu,omitempty"`
	Peak   *float64  `json:"cpu_peak,omitempty"`
	Memory *Memory   `json:"memory,omitempty"`
	Swap   *Swap     `json:"swap,omitempty"`
	Disk   *Disk     `json:"disk,omitempty"`
}
type Runner struct {
	Scope  string   `json:"scope"`
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	OS     string   `json:"os"`
	Status string   `json:"status"`
	Busy   bool     `json:"busy"`
	Labels []string `json:"labels"`
	HostID string   `json:"host_id,omitempty"`
}
type Job struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Attempt    int    `json:"attempt"`
	Repo       string `json:"repository"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	RunnerID   int64  `json:"runner_id"`
	RunnerName string `json:"runner_name"`
	URL        string `json:"url"`
	Started    string `json:"started"`
}
type Source struct {
	Updated *time.Time `json:"updated,omitempty"`
	Error   string     `json:"error,omitempty"`
	Stale   bool       `json:"stale"`
}
type View struct {
	Time    time.Time         `json:"time"`
	Hosts   []Host            `json:"hosts"`
	Runners []Runner          `json:"runners"`
	Jobs    []Job             `json:"jobs"`
	Sources map[string]Source `json:"sources"`
}
type State struct {
	mu       sync.RWMutex
	hosts    map[string]Host
	history  map[string][]Point
	runners  map[string][]Runner
	jobs     map[string][]Job
	sources  map[string]Source
	now      func() time.Time
	maxHosts int
}

func New() *State {
	return &State{hosts: map[string]Host{}, history: map[string][]Point{}, runners: map[string][]Runner{}, jobs: map[string][]Job{}, sources: map[string]Source{}, now: time.Now, maxHosts: 128}
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var scope = regexp.MustCompile(`^(orgs/[a-zA-Z0-9_-]+|repos/[a-zA-Z0-9_-]+/[a-zA-Z0-9_.-]+)$`)

func ValidScope(s string) bool { return len(s) <= 256 && scope.MatchString(s) }
func finite(p *float64) bool {
	return p == nil || (!math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 100)
}
func (s *State) Ingest(r Report) error {
	if r.Schema != 1 || !identifier.MatchString(r.ID) || len(r.Name) < 1 || len(r.Name) > 128 || len(r.OS) > 32 || len(r.Arch) > 32 || r.Cores < 1 || r.Cores > 4096 || len(r.Runners) > 32 || !finite(r.CPU) || !finite(r.CPUPeak) {
		return errors.New("invalid host report")
	}
	if r.Load != nil && (math.IsNaN(*r.Load) || math.IsInf(*r.Load, 0) || *r.Load < 0 || *r.Load > 1e6) {
		return errors.New("invalid load")
	}
	if r.Memory != nil && (r.Memory.Used > r.Memory.Total || r.Memory.Available > r.Memory.Total) {
		return errors.New("invalid memory counters")
	}
	if r.Disk != nil && r.Disk.Free > r.Disk.Total {
		return errors.New("invalid disk counters")
	}
	if r.Swap != nil && r.Swap.Used > r.Swap.Total {
		return errors.New("invalid swap counters")
	}
	for _, ref := range r.Runners {
		if !ValidScope(ref.Scope) || ref.ID <= 0 {
			return errors.New("invalid runner reference")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[r.ID]; !ok && len(s.hosts) >= s.maxHosts {
		return errors.New("host limit reached")
	}
	now := s.now().UTC()
	s.hosts[r.ID] = Host{Report: r, Received: now}
	points := s.history[r.ID]
	cutoff := now.Add(-2 * time.Hour)
	i := 0
	for i < len(points) && points[i].Time.Before(cutoff) {
		i++
	}
	points = points[i:]
	// Bound points even if a collector sends faster than the expected 15 seconds.
	if len(points) >= 480 {
		points = points[len(points)-479:]
	}
	points = append(points, Point{Time: now, CPU: r.CPU, Peak: r.CPUPeak, Memory: r.Memory, Swap: r.Swap, Disk: r.Disk})
	s.history[r.ID] = points
	return nil
}
func (s *State) Update(key string, runners []Runner, jobs []Job, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.sources[key]
	if err != nil {
		old.Error = err.Error()
		old.Stale = true
		s.sources[key] = old
		return
	}
	now := s.now().UTC()
	s.sources[key] = Source{Updated: &now}
	if runners != nil {
		s.runners[key] = runners
	}
	if jobs != nil {
		s.jobs[key] = jobs
	}
}
func (s *State) Snapshot() View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now().UTC()
	v := View{Time: now, Hosts: []Host{}, Runners: []Runner{}, Jobs: []Job{}, Sources: map[string]Source{}}
	refs := map[RunnerRef][]string{}
	for _, host := range s.hosts {
		host.Stale = now.Sub(host.Received) > 45*time.Second
		v.Hosts = append(v.Hosts, host)
		for _, ref := range host.Runners {
			refs[ref] = append(refs[ref], host.ID)
		}
	}
	for _, list := range s.runners {
		for _, r := range list {
			matches := refs[RunnerRef{r.Scope, r.ID}]
			if len(matches) == 1 {
				r.HostID = matches[0]
			}
			v.Runners = append(v.Runners, r)
		}
	}
	for _, list := range s.jobs {
		v.Jobs = append(v.Jobs, list...)
	}
	for key, source := range s.sources {
		source.Stale = source.Error != "" || source.Updated == nil || now.Sub(*source.Updated) > 90*time.Second
		v.Sources[key] = source
	}
	sort.Slice(v.Hosts, func(i, j int) bool { return v.Hosts[i].Name < v.Hosts[j].Name })
	sort.Slice(v.Runners, func(i, j int) bool {
		return v.Runners[i].Scope+v.Runners[i].Name < v.Runners[j].Scope+v.Runners[j].Name
	})
	return v
}
func (s *State) History(id string, minutes int) ([]Point, error) {
	if minutes < 1 || minutes > 120 {
		return nil, errors.New("minutes must be between 1 and 120")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.hosts[id]; !ok {
		return nil, errors.New("host not found")
	}
	out := []Point{}
	cutoff := s.now().Add(-time.Duration(minutes) * time.Minute)
	for _, p := range s.history[id] {
		if !p.Time.Before(cutoff) {
			out = append(out, p)
		}
	}
	return out, nil
}
