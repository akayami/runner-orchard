package collector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityAndNonSecretRegistration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id")
	one, e := identity(path)
	if e != nil {
		t.Fatal(e)
	}
	two, e := identity(path)
	if e != nil || one != two {
		t.Fatal("unstable identity")
	}
	if e = os.WriteFile(filepath.Join(dir, ".runner"), append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"agentId":5,"gitHubUrl":"https://github.com/example"}`)...), 0600); e != nil {
		t.Fatal(e)
	}
	refs, drained := runnerRefs([]string{dir})
	if len(refs) != 1 || refs[0].Scope != "orgs/example" || *drained {
		t.Fatal(refs, drained)
	}
	_ = os.WriteFile(filepath.Join(dir, ".drained"), nil, 0600)
	_, drained = runnerRefs([]string{dir})
	if !*drained {
		t.Fatal("drain marker ignored")
	}
}
