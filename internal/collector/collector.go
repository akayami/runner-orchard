package collector

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
	"github.com/akayami/runner-orchard/internal/worker"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

type Config struct {
	Endpoint    string   `json:"endpoint"`
	Name        string   `json:"name"`
	IDFile      string   `json:"id_file"`
	RunnerDirs  []string `json:"runner_dirs"`
	PowerStatus string   `json:"power_status"`
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
func identity(path string) (string, error) {
	path = expand(path)
	if b, e := os.ReadFile(path); e == nil {
		s := strings.TrimSpace(string(b))
		if len(s) != 24 {
			return "", fmt.Errorf("invalid existing collector identity")
		}
		if _, e := hex.DecodeString(s); e != nil {
			return "", fmt.Errorf("invalid existing collector identity")
		}
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(id + "\n")
	return id, err
}
func readJSON(path string, out any) error {
	f, err := os.Open(expand(path))
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return fmt.Errorf("metadata too large or unreadable")
	}
	// GitHub's runner writes UTF-8 metadata with a BOM on some platforms.
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	return json.Unmarshal(data, out)
}
func runnerRefs(dirs []string) ([]fleet.RunnerRef, *bool) {
	out := []fleet.RunnerRef{}
	var drained *bool
	for _, dir := range dirs {
		dir = expand(dir)
		var r struct {
			ID  int64  `json:"agentId"`
			URL string `json:"gitHubUrl"`
		}
		if readJSON(filepath.Join(dir, ".runner"), &r) != nil {
			continue
		}
		u, e := url.Parse(r.URL)
		if e != nil || u.Host != "github.com" {
			continue
		}
		path := strings.Trim(u.Path, "/")
		scope := "orgs/" + path
		if strings.Contains(path, "/") {
			scope = "repos/" + path
		}
		if fleet.ValidScope(scope) && r.ID > 0 {
			out = append(out, fleet.RunnerRef{Scope: scope, ID: r.ID})
			if drained == nil {
				v := false
				drained = &v
			}
			if _, err := os.Stat(filepath.Join(dir, ".drained")); err == nil {
				*drained = true
			}
		}
	}
	return out, drained
}
func output(ctx context.Context, command string, args ...string) string {
	bounded, done := context.WithTimeout(ctx, 2*time.Second)
	defer done()
	data, err := exec.CommandContext(bounded, command, args...).Output()
	if err != nil {
		return ""
	}
	return string(data)
}
func power(ctx context.Context, path string) *fleet.Power {
	if runtime.GOOS == "windows" {
		return &fleet.Power{AC: worker.ACPower()}
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	p := &fleet.Power{}
	var status struct {
		AC      *bool   `json:"ac"`
		Active  *bool   `json:"active"`
		Healthy *bool   `json:"healthy"`
		Updated float64 `json:"updated"`
	}
	if path != "" && readJSON(path, &status) == nil {
		p.AC = status.AC
		p.Active = status.Active
		p.Healthy = status.Healthy
		if time.Now().Unix()-int64(status.Updated) > 15 {
			v := false
			p.Healthy = &v
		}
	}
	text := output(ctx, "/usr/bin/pmset", "-g", "batt")
	if strings.Contains(text, "Now drawing from") {
		v := strings.Contains(text, "Now drawing from 'AC Power'")
		p.AC = &v
	}
	text = output(ctx, "/usr/sbin/ioreg", "-r", "-c", "IOPMrootDomain", "-d", "1")
	if strings.Contains(text, `"AppleClamshellState" = Yes`) {
		v := true
		p.LidClosed = &v
	} else if strings.Contains(text, `"AppleClamshellState" = No`) {
		v := false
		p.LidClosed = &v
	}
	return p
}

func diskPath(runnerDirs []string) string {
	if runtime.GOOS != "windows" {
		return "/"
	}
	for _, dir := range runnerDirs {
		if info, err := os.Stat(expand(dir)); err == nil && info.IsDir() {
			return expand(dir)
		}
	}
	return ""
}

func Run(ctx context.Context, c Config) error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("endpoint must be an HTTP(S) telemetry URL without credentials")
	}
	if u.Path != "/api/telemetry" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("endpoint path must be /api/telemetry")
	}
	if len(c.RunnerDirs) > 32 {
		return fmt.Errorf("too many runner directories")
	}
	if c.IDFile == "" {
		c.IDFile = "~/.local/share/runner-orchard/collector.id"
	}
	id, err := identity(c.IDFile)
	if err != nil {
		return fmt.Errorf("cannot initialize collector identity: %w", err)
	}
	if c.Name == "" {
		c.Name, _ = os.Hostname()
	}
	if len(c.Name) > 128 || c.Name == "" {
		return fmt.Errorf("invalid display name")
	}
	client := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	_, _ = cpu.PercentWithContext(ctx, 0, false)
	var latest, peak *float64
	next := time.Now().Add(2 * time.Second)
	failures := 0
	wasFailing := false
	var osVersion string
	if runtime.GOOS == "windows" {
		_, _, osVersion, _ = host.PlatformInformationWithContext(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if values, e := cpu.PercentWithContext(ctx, 0, false); e == nil && len(values) > 0 {
				v := values[0]
				latest = &v
				if peak == nil || v > *peak {
					copy := v
					peak = &copy
				}
			}
			if time.Now().Before(next) {
				continue
			}
			refs, drained := runnerRefs(c.RunnerDirs)
			r := fleet.Report{Schema: 1, ID: id, Name: c.Name, OS: runtime.GOOS, OSVersion: osVersion, Arch: runtime.GOARCH, Cores: runtime.NumCPU(), CPU: latest, CPUPeak: peak, Runners: refs, Drained: drained}
			if m, e := mem.VirtualMemoryWithContext(ctx); e == nil {
				r.Memory = &fleet.Memory{Total: m.Total, Used: m.Used, Available: m.Available}
			}
			if s, e := mem.SwapMemoryWithContext(ctx); e == nil {
				r.Swap = &fleet.Swap{Used: s.Used, Total: s.Total, In: s.Sin, Out: s.Sout}
			}
			if path := diskPath(c.RunnerDirs); path != "" {
				if d, e := disk.UsageWithContext(ctx, path); e == nil {
					r.Disk = &fleet.Disk{Total: d.Total, Free: d.Free}
				}
			}
			if l, e := load.AvgWithContext(ctx); e == nil {
				r.Load = &l.Load1
			}
			r.Uptime, _ = host.UptimeWithContext(ctx)
			r.Power = power(ctx, c.PowerStatus)
			data, e := json.Marshal(r)
			if e != nil {
				return fmt.Errorf("cannot encode host telemetry")
			}
			req, e := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(data))
			if e != nil {
				return e
			}
			req.Header.Set("Content-Type", "application/json")
			resp, e := client.Do(req)
			ok := false
			if e == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				ok = resp.StatusCode == 204
			}
			if ok {
				if wasFailing {
					log.Print("telemetry connection recovered")
				}
				failures = 0
				wasFailing = false
				next = time.Now().Add(15 * time.Second)
			} else {
				failures++
				if !wasFailing {
					log.Print("telemetry unavailable; backing off without affecting builds")
				}
				wasFailing = true
				delay := 15 * time.Second * time.Duration(1<<min(failures, 4))
				next = time.Now().Add(delay)
			}
			latest = nil
			peak = nil
		}
	}
}
