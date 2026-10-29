// Package registry loads the optional worker allowlist (workers.yaml). Workers register
// themselves with the controller; when an allowlist exists, only the enabled workers it
// lists may register, and its tags are added to theirs.
package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Entry is one allowed worker.
type Entry struct {
	WorkerID string   `yaml:"worker_id"`
	Enabled  *bool    `yaml:"enabled"` // default true
	Tags     []string `yaml:"tags"`    // added to the tags the worker reports
}

// Allowlist maps worker IDs to their entries. A nil Allowlist allows every worker.
type Allowlist map[string]Entry

// Load reads path. A missing file, or an empty path, yields a nil Allowlist.
func Load(path string) (Allowlist, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var f struct {
		Workers []Entry `yaml:"workers"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	list := Allowlist{}
	for _, e := range f.Workers {
		if e.WorkerID == "" {
			return nil, fmt.Errorf("%s: every worker needs a worker_id", path)
		}
		list[e.WorkerID] = e
	}
	return list, nil
}

// Check reports whether workerID may register, and the extra tags it gets.
func (a Allowlist) Check(workerID string) (tags []string, err error) {
	if a == nil {
		return nil, nil
	}
	e, ok := a[workerID]
	if !ok {
		return nil, fmt.Errorf("worker %q is not in the allowlist", workerID)
	}
	if e.Enabled != nil && !*e.Enabled {
		return nil, fmt.Errorf("worker %q is disabled in the allowlist", workerID)
	}
	return e.Tags, nil
}
