package fleet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Group struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
	Public     bool   `json:"allows_public_repositories"`
}
type RunnerPackage struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	URL          string `json:"download_url"`
	SHA256       string `json:"sha256_checksum"`
}

func (g *GitHub) Groups(ctx context.Context, org string) ([]Group, error) {
	if !ValidScope("orgs/" + org) {
		return nil, fmt.Errorf("invalid organization")
	}
	all := []Group{}
	for page := 1; page <= 20; page++ {
		var result struct {
			Groups []Group `json:"runner_groups"`
		}
		if err := g.get(ctx, fmt.Sprintf("/orgs/%s/actions/runner-groups?per_page=100&page=%d", org, page), &result); err != nil {
			return nil, err
		}
		all = append(all, result.Groups...)
		if len(result.Groups) < 100 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("group pagination limit exceeded")
}

// RunnerPackageOS preserves the implicit macOS default used by older clients.
func RunnerPackageOS(os string) string {
	if os == "" {
		return "osx"
	}
	return os
}

func (g *GitHub) Provision(ctx context.Context, org, group, name, arch string) (string, RunnerPackage, error) {
	return g.ProvisionPlatform(ctx, org, group, name, "osx", arch)
}

func (g *GitHub) ProvisionPlatform(ctx context.Context, org, group, name, os, arch string) (string, RunnerPackage, error) {
	var empty RunnerPackage
	if (os != "osx" && os != "win") || (arch != "x64" && !(os == "osx" && arch == "arm64")) {
		return "", empty, fmt.Errorf("unsupported runner platform %s/%s", os, arch)
	}
	groups, err := g.Groups(ctx, org)
	if err != nil {
		return "", empty, err
	}
	valid := false
	for _, item := range groups {
		if item.Name == group && item.Visibility == "selected" && !item.Public {
			valid = true
		}
	}
	if !valid {
		return "", empty, fmt.Errorf("group must restrict access to selected private repositories")
	}
	runners, err := g.Runners(ctx, "orgs/"+org)
	if err != nil {
		return "", empty, err
	}
	for _, r := range runners {
		if strings.EqualFold(r.Name, name) {
			return "", empty, fmt.Errorf("runner name already exists; existing registrations are never replaced")
		}
	}
	var packages []RunnerPackage
	if err = g.get(ctx, "/orgs/"+org+"/actions/runners/downloads", &packages); err != nil {
		return "", empty, err
	}
	var pkg RunnerPackage
	for _, p := range packages {
		if p.OS == os && p.Architecture == arch {
			pkg = p
			break
		}
	}
	if checksum, err := hex.DecodeString(pkg.SHA256); pkg.URL == "" || err != nil || len(checksum) != 32 {
		return "", empty, fmt.Errorf("verified %s/%s runner package unavailable", os, arch)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, "POST", g.Base+"/orgs/"+org+"/actions/runners/registration-token", nil)
	if err != nil {
		return "", empty, err
	}
	token := g.Token()
	if token == "" {
		return "", empty, fmt.Errorf("console GitHub credential required")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "runner-orchard")
	response, err := g.Client.Do(req)
	if err != nil {
		return "", empty, fmt.Errorf("registration credential request unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 201 {
		return "", empty, fmt.Errorf("registration credential request denied (HTTP %d)", response.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result) != nil || result.Token == "" {
		return "", empty, fmt.Errorf("invalid registration credential response")
	}
	return result.Token, pkg, nil
}
