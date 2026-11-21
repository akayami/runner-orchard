package worker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
)

// RemoteControl uses only a single runner's lifecycle credential.
type RemoteControl struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewRemoteControl(endpoint, token string) (*RemoteControl, error) {
	u, err := url.Parse(endpoint)
	raw, tokenErr := hex.DecodeString(token)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || tokenErr != nil || len(raw) != 32 {
		return nil, fmt.Errorf("HTTPS worker lifecycle endpoint and worker credential required")
	}
	id, ok := strings.CutPrefix(u.Path, "/api/windows-workers/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return nil, fmt.Errorf("invalid worker lifecycle endpoint")
	}
	client := &http.Client{
		Timeout:       27 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &RemoteControl{endpoint: endpoint, token: token, client: client}, nil
}

func (c *RemoteControl) SetEligible(ctx context.Context, eligible bool) (fleet.WindowsEligibility, error) {
	var status fleet.WindowsEligibility
	body := strings.NewReader(fmt.Sprintf(`{"eligible":%t}`, eligible))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, body)
	if err != nil {
		return status, fmt.Errorf("invalid lifecycle request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return status, fmt.Errorf("worker lifecycle endpoint unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("worker lifecycle endpoint returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&status); err != nil || status.Eligible != eligible {
		return fleet.WindowsEligibility{}, fmt.Errorf("unexpected worker lifecycle response")
	}
	return status, nil
}
