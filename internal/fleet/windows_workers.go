package fleet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type WindowsEligibility struct {
	Eligible bool  `json:"eligible"`
	Busy     bool  `json:"busy"`
	Online   bool  `json:"online"`
	RunnerID int64 `json:"runner_id"`
}

// SetRoutingEligibility changes only the platform pool label on the bound runner.
func (g *GitHub) SetRoutingEligibility(ctx context.Context, org string, id int64, platform string, eligible bool) (WindowsEligibility, error) {
	if !ValidScope("orgs/"+org) || id <= 0 {
		return WindowsEligibility{}, fmt.Errorf("invalid Windows runner identity")
	}
	base := fmt.Sprintf("/orgs/%s/actions/runners/%d", org, id)
	var runner struct {
		OS     string `json:"os"`
		Status string `json:"status"`
		Busy   bool   `json:"busy"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := g.get(ctx, base, &runner); err != nil {
		return WindowsEligibility{}, err
	}
	var label string
	switch {
	case platform == "windows" && (strings.EqualFold(runner.OS, "windows") || strings.EqualFold(runner.OS, "win")):
		label = "windows-build"
	case platform == "mac" && strings.EqualFold(runner.OS, "macOS"):
		label = "apple-build"
	default:
		return WindowsEligibility{}, fmt.Errorf("runner platform mismatch")
	}
	hasLabel := false
	for _, item := range runner.Labels {
		if strings.EqualFold(item.Name, label) {
			hasLabel = true
			break
		}
	}
	if eligible != hasLabel {
		method, path := http.MethodDelete, base+"/labels/"+label
		var body io.Reader
		if eligible {
			method, path = http.MethodPost, base+"/labels"
			body = strings.NewReader(fmt.Sprintf(`{"labels":[%q]}`, label))
		}
		req, err := http.NewRequestWithContext(ctx, method, g.Base+path, body)
		if err != nil {
			return WindowsEligibility{}, err
		}
		token := g.Token()
		if token == "" {
			return WindowsEligibility{}, fmt.Errorf("console GitHub credential required")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "runner-orchard")
		if eligible {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := g.Client.Do(req)
		if err != nil {
			return WindowsEligibility{}, fmt.Errorf("runner label request unavailable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return WindowsEligibility{}, fmt.Errorf("runner label request failed (HTTP %d)", resp.StatusCode)
		}
	}
	return WindowsEligibility{Eligible: eligible, Busy: runner.Busy, Online: runner.Status == "online", RunnerID: id}, nil
}

// RemoveWindowsWorker only removes an offline, idle, de-routed runner.
func (g *GitHub) RemoveWindowsWorker(ctx context.Context, org string, id int64) error {
	status, err := g.SetRoutingEligibility(ctx, org, id, "windows", false)
	if err != nil {
		return err
	}
	if status.Busy || status.Online {
		return fmt.Errorf("Windows runner must be offline before removal")
	}
	path := fmt.Sprintf("/orgs/%s/actions/runners/%d", org, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, g.Base+path, nil)
	if err != nil {
		return err
	}
	token := g.Token()
	if token == "" {
		return fmt.Errorf("console GitHub credential required")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "runner-orchard")
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("runner removal request unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("runner removal failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}
