package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workers.yaml")
	yaml := `workers:
  - worker_id: a
    tags: [gpu]
  - worker_id: b
    enabled: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if tags, err := list.Check("a"); err != nil || len(tags) != 1 || tags[0] != "gpu" {
		t.Errorf("Check(a) = %v, %v; want [gpu], nil", tags, err)
	}
	if _, err := list.Check("b"); err == nil {
		t.Error("Check(b) allowed a disabled worker")
	}
	if _, err := list.Check("c"); err == nil {
		t.Error("Check(c) allowed an unlisted worker")
	}
}

func TestMissingFileAllowsEveryone(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.yaml")} {
		list, err := Load(path)
		if err != nil || list != nil {
			t.Fatalf("Load(%q) = %v, %v; want nil, nil", path, list, err)
		}
		if _, err := list.Check("anyone"); err != nil {
			t.Errorf("nil allowlist refused a worker: %v", err)
		}
	}
}

func TestRepoAllowlist(t *testing.T) {
	list, err := Load("../../workers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := list.Check("worker-1"); err != nil {
		t.Errorf("workers.yaml should allow worker-1: %v", err)
	}
}
