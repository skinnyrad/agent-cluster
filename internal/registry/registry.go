// Package registry loads the worker fleet from workers.yaml.
package registry

import (
	"errors"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

type fileEntry struct {
	WorkerID string   `yaml:"worker_id"`
	URL      string   `yaml:"url"`
	ModelID  string   `yaml:"model_id"`
	Enabled  *bool    `yaml:"enabled"`
	Tags     []string `yaml:"tags"`
}

type file struct {
	Workers []fileEntry `yaml:"workers"`
}

// Load reads path and returns the enabled workers. A missing file yields no workers.
// The file is re-read on every call, so edits take effect without a restart.
func Load(path string) ([]schemas.WorkerInfo, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}

	var workers []schemas.WorkerInfo
	for _, e := range f.Workers {
		enabled := e.Enabled == nil || *e.Enabled
		if !enabled {
			continue
		}
		if e.WorkerID == "" || e.URL == "" {
			return nil, errors.New("workers.yaml: every worker needs worker_id and url")
		}
		tags := e.Tags
		if tags == nil {
			tags = []string{}
		}
		workers = append(workers, schemas.WorkerInfo{
			WorkerID: e.WorkerID,
			URL:      e.URL,
			ModelID:  e.ModelID,
			Enabled:  true,
			Tags:     tags,
		})
	}
	return workers, nil
}
