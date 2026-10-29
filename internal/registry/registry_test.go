package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workers.yaml")
	yaml := `workers:
  - worker_id: a
    url: http://localhost:8001
    model_id: gemma4
    enabled: true
    tags: [default]
  - worker_id: b
    url: http://localhost:8002
    enabled: false
  - worker_id: c
    url: http://localhost:8003
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	workers, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("got %d workers, want 2 (disabled one skipped): %+v", len(workers), workers)
	}
	if workers[0].WorkerID != "a" || workers[0].ModelID != "gemma4" || workers[0].Tags[0] != "default" {
		t.Errorf("unexpected first worker: %+v", workers[0])
	}
	if workers[1].WorkerID != "c" || !workers[1].Enabled || workers[1].Tags == nil {
		t.Errorf("worker without enabled/tags should default to enabled with empty tags: %+v", workers[1])
	}
}

func TestLoadMissingFile(t *testing.T) {
	workers, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil || len(workers) != 0 {
		t.Fatalf("Load(missing) = %v, %v; want no workers and no error", workers, err)
	}
}

func TestLoadRepoRegistry(t *testing.T) {
	workers, err := Load("../../workers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) == 0 {
		t.Fatal("expected at least one enabled worker in workers.yaml")
	}
}
