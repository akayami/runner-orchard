package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type GitHub struct {
	Base       string
	Token      func() string
	Client     *http.Client
	etags      map[string]string
	cache      map[string][]byte
	cacheBytes int
	RetryAfter time.Time
}

func NewGitHub(token func() string) *GitHub {
	return &GitHub{Base: "https://api.github.com", Token: token, Client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}, etags: map[string]string{}, cache: map[string][]byte{}}
}
func (g *GitHub) get(ctx context.Context, path string, out any) error {
	if time.Now().Before(g.RetryAfter) {
		return fmt.Errorf("GitHub rate limit backoff until %s", g.RetryAfter.UTC().Format(time.RFC3339))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", g.Base+path, nil)
	if err != nil {
		return err
	}
	if token := g.Token(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "runner-orchard")
	if etag := g.etags[path]; etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 304 {
		if data, ok := g.cache[path]; ok {
			return json.Unmarshal(data, out)
		}
		return fmt.Errorf("GitHub cache unavailable")
	}
	if resp.StatusCode == 429 || resp.StatusCode == 403 {
		delay := 60 * time.Second
		if sec, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil {
			delay = time.Duration(sec) * time.Second
		}
		if reset, e := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); e == nil && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			delay = time.Until(time.Unix(reset, 0))
		}
		if delay < time.Minute {
			delay = time.Minute
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		g.RetryAfter = time.Now().Add(delay)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub HTTP %d for %s", resp.StatusCode, strings.Split(path, "?")[0])
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 {
		return fmt.Errorf("GitHub response too large or unreadable")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid GitHub response")
	}
	if len(g.cache) >= 512 || g.cacheBytes+len(data)-len(g.cache[path]) > 8*1024*1024 {
		g.cache = map[string][]byte{}
		g.etags = map[string]string{}
		g.cacheBytes = 0
	}
	g.cacheBytes += len(data) - len(g.cache[path])
	g.cache[path] = data
	g.etags[path] = resp.Header.Get("ETag")
	return nil
}
func (g *GitHub) Runners(ctx context.Context, scope string) ([]Runner, error) {
	if !ValidScope(scope) {
		return nil, fmt.Errorf("invalid GitHub scope")
	}
	out := []Runner{}
	for page := 1; page <= 20; page++ {
		var data struct {
			Total   int `json:"total_count"`
			Runners []struct {
				ID     int64  `json:"id"`
				Name   string `json:"name"`
				OS     string `json:"os"`
				Status string `json:"status"`
				Busy   bool   `json:"busy"`
				Labels []struct {
					Name string `json:"name"`
				} `json:"labels"`
			} `json:"runners"`
		}
		if err := g.get(ctx, fmt.Sprintf("/%s/actions/runners?per_page=100&page=%d", scope, page), &data); err != nil {
			return nil, err
		}
		for _, r := range data.Runners {
			labels := []string{}
			for _, l := range r.Labels {
				labels = append(labels, l.Name)
			}
			out = append(out, Runner{Scope: scope, ID: r.ID, Name: r.Name, OS: r.OS, Status: r.Status, Busy: r.Busy, Labels: labels})
		}
		if len(data.Runners) < 100 || len(out) >= data.Total {
			return out, nil
		}
	}
	return nil, fmt.Errorf("runner pagination limit exceeded")
}
func (g *GitHub) Jobs(ctx context.Context, repo string) ([]Job, error) {
	if !ValidScope("repos/" + repo) {
		return nil, fmt.Errorf("invalid repository")
	}
	out := []Job{}
	// Explicitly bounded scan. Queue rows have no fabricated runner assignment.
	for _, status := range []string{"in_progress", "queued"} {
		var runs struct {
			Total int `json:"total_count"`
			Runs  []struct {
				ID      int64 `json:"id"`
				Attempt int   `json:"run_attempt"`
			} `json:"workflow_runs"`
		}
		if err := g.get(ctx, "/repos/"+repo+"/actions/runs?status="+url.QueryEscape(status)+"&per_page=20", &runs); err != nil {
			return nil, err
		}
		if runs.Total > 20 {
			return nil, fmt.Errorf("active run scan exceeds configured bound (20 per state)")
		}
		for _, run := range runs.Runs {
			for page := 1; page <= 10; page++ {
				var jobs struct {
					Total int `json:"total_count"`
					Jobs  []struct {
						ID         int64  `json:"id"`
						Name       string `json:"name"`
						Status     string `json:"status"`
						RunnerID   int64  `json:"runner_id"`
						RunnerName string `json:"runner_name"`
						URL        string `json:"html_url"`
						Started    string `json:"started_at"`
					} `json:"jobs"`
				}
				path := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", repo, run.ID, run.Attempt, page)
				if err := g.get(ctx, path, &jobs); err != nil {
					return nil, err
				}
				for _, j := range jobs.Jobs {
					if j.Status == "in_progress" || j.Status == "queued" {
						out = append(out, Job{ID: j.ID, RunID: run.ID, Attempt: run.Attempt, Repo: repo, Name: j.Name, Status: j.Status, RunnerID: j.RunnerID, RunnerName: j.RunnerName, URL: j.URL, Started: j.Started})
						if len(out) > 2000 {
							return nil, fmt.Errorf("active job count exceeds bound (2000)")
						}
					}
				}
				if jobs.Total <= page*100 {
					break
				}
				if page == 10 {
					return nil, fmt.Errorf("job pagination limit exceeded")
				}
			}
		}
	}
	return out, nil
}
func (g *GitHub) Poll(ctx context.Context, state *State, scopes, repos []string) {
	for _, scope := range scopes {
		if ctx.Err() != nil {
			return
		}
		r, err := g.Runners(ctx, scope)
		state.Update(scope, r, nil, err)
	}
	for _, repo := range repos {
		if ctx.Err() != nil {
			return
		}
		j, err := g.Jobs(ctx, repo)
		state.Update("jobs/"+repo, nil, j, err)
	}
}
