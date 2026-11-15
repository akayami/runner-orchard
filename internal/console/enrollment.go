package console

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
)

type Profile struct {
	ID           string   `json:"id"`
	Organization string   `json:"organization"`
	Group        string   `json:"runner_group"`
	Labels       []string `json:"labels"`
}
type Provisioning struct {
	Enabled  bool      `json:"enabled"`
	Profiles []Profile `json:"profiles"`
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var safeLabel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func (p Provisioning) validate() error {
	if !p.Enabled {
		return nil
	}
	if len(p.Profiles) < 1 || len(p.Profiles) > 32 {
		return fmt.Errorf("provisioning needs 1–32 profiles")
	}
	seen := map[string]bool{}
	for _, profile := range p.Profiles {
		if !safeName.MatchString(profile.ID) || seen[profile.ID] || !safeName.MatchString(profile.Organization) || profile.Group == "" || len(profile.Group) > 128 || len(profile.Labels) > 32 {
			return fmt.Errorf("invalid provisioning profile")
		}
		seen[profile.ID] = true
		required := false
		for _, label := range profile.Labels {
			if !safeLabel.MatchString(label) {
				return fmt.Errorf("invalid profile label")
			}
			switch strings.ToLower(label) {
			case "arm64", "x64", "arm", "macos", "linux", "windows", "self-hosted":
				return fmt.Errorf("host labels are assigned by GitHub, not profiles")
			}
			if label == "apple-build" {
				required = true
			}
		}
		if !required {
			return fmt.Errorf("profile must include apple-build")
		}
	}
	return nil
}

type Enrollment struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Arch      string    `json:"architecture"`
	Developer string    `json:"developer_dir"`
	Created   time.Time `json:"created"`
	Status    string    `json:"status"`
	Profile   string    `json:"profile,omitempty"`
	Error     string    `json:"error,omitempty"`
	secret    [32]byte
}
type enrollments struct {
	mu       sync.Mutex
	entries  map[string]*Enrollment
	profiles []Profile
	now      func() time.Time
}

func randomID() string {
	var b [12]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic("secure randomness unavailable")
	}
	return hex.EncodeToString(b[:])
}
func (e *enrollments) prune() {
	for id, item := range e.entries {
		if e.now().Sub(item.Created) > 10*time.Minute {
			delete(e.entries, id)
		}
	}
}
func (e *enrollments) profile(id string) (Profile, bool) {
	for _, p := range e.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}
func decodeBody(r *http.Request, v any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return fmt.Errorf("application/json required")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("one JSON object required")
	}
	return nil
}
func registerEnrollment(mux *http.ServeMux, c Config, g *fleet.GitHub) {
	enabled := c.Provisioning.Enabled && g != nil
	var e *enrollments
	if enabled {
		e = &enrollments{entries: map[string]*Enrollment{}, profiles: c.Provisioning.Profiles, now: time.Now}
	}
	mux.HandleFunc("GET /api/enrollment", func(w http.ResponseWriter, r *http.Request) {
		if !enabled {
			jsonResponse(w, map[string]any{"enabled": false, "requests": []Enrollment{}, "profiles": []Profile{}})
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.prune()
		items := []Enrollment{}
		for _, item := range e.entries {
			items = append(items, *item)
		}
		jsonResponse(w, map[string]any{"enabled": true, "requests": items, "profiles": e.profiles})
	})
	if !enabled {
		return
	}
	// Store is private to this handler instance and contains no persistent credentials.
	mux.HandleFunc("POST /api/enrollment", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name      string `json:"name"`
			Arch      string `json:"architecture"`
			Developer string `json:"developer_dir"`
			Secret    string `json:"secret"`
		}
		if decodeBody(r, &in) != nil || !safeName.MatchString(in.Name) || (in.Arch != "arm64" && in.Arch != "x64") || !strings.HasPrefix(in.Developer, "/") || len(in.Developer) > 512 || strings.ContainsAny(in.Developer, "\r\n") || len(in.Secret) != 64 {
			http.Error(w, "invalid enrollment", 400)
			return
		}
		secret, err := hex.DecodeString(in.Secret)
		if err != nil {
			http.Error(w, "invalid enrollment secret", 400)
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.prune()
		if len(e.entries) >= 64 {
			http.Error(w, "enrollment queue full", 429)
			return
		}
		id := randomID()
		e.entries[id] = &Enrollment{ID: id, Name: in.Name, Arch: in.Arch, Developer: in.Developer, Created: e.now().UTC(), Status: "pending", secret: sha256.Sum256(secret)}
		jsonResponse(w, map[string]any{"id": id, "expires_in": 600})
	})
	mux.HandleFunc("POST /api/enrollment/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Profile string `json:"profile"`
			Name    string `json:"name"`
		}
		if decodeBody(r, &in) != nil || !safeName.MatchString(in.Name) {
			http.Error(w, "invalid approval", 400)
			return
		}
		if _, ok := e.profile(in.Profile); !ok {
			http.Error(w, "unknown provisioning profile", 400)
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.prune()
		item, ok := e.entries[r.PathValue("id")]
		if !ok || item.Status != "pending" {
			http.Error(w, "request unavailable", 409)
			return
		}
		item.Profile = in.Profile
		item.Name = in.Name
		item.Status = "approved"
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /api/enrollment/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		secret, err := hex.DecodeString(raw)
		if err != nil || len(secret) != 32 {
			http.Error(w, "invalid enrollment credential", 401)
			return
		}
		hash := sha256.Sum256(secret)
		e.mu.Lock()
		e.prune()
		item, ok := e.entries[r.PathValue("id")]
		if !ok || subtle.ConstantTimeCompare(hash[:], item.secret[:]) != 1 {
			e.mu.Unlock()
			http.Error(w, "request unavailable", 404)
			return
		}
		if item.Status == "pending" {
			e.mu.Unlock()
			jsonResponse(w, map[string]string{"status": "pending"})
			return
		}
		if item.Status != "approved" {
			status := item.Status
			e.mu.Unlock()
			jsonResponse(w, map[string]string{"status": status})
			return
		}
		copy := *item
		item.Status = "claiming"
		e.mu.Unlock()
		profile, _ := e.profile(copy.Profile)
		token, pkg, err := g.Provision(r.Context(), profile.Organization, profile.Group, copy.Name, copy.Arch)
		e.mu.Lock()
		if err != nil {
			item.Status = "failed"
			item.Error = err.Error()
		} else {
			item.Status = "claimed"
		}
		e.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		jsonResponse(w, map[string]any{"status": "ready", "runner": map[string]any{"organization": profile.Organization, "name": copy.Name, "runner_group": profile.Group, "labels": profile.Labels, "developer_dir": copy.Developer}, "registration_token": token, "package": pkg})
	})
}
