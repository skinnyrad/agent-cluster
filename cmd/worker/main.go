// Command worker runs one generic, role-agnostic agent node.
//
//	WORKER_ID=worker-1 MODEL_ID=gemma4 go run ./cmd/worker --port 8001
package main

import (
	"flag"

	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/schemas"
	"github.com/skinnyrad/agent-cluster/internal/worker"
)

func main() {
	host, port := httputil.ListenFlags(8001)
	flag.Parse()

	id := httputil.Getenv("WORKER_ID", "worker-1")
	handler := worker.New(worker.Config{
		WorkerID: id,
		ModelID:  httputil.Getenv("MODEL_ID", "gemma4"),
		Status:   schemas.WorkerStatus(httputil.Getenv("WORKER_STATUS", "idle")),
	})
	httputil.ListenAndServe("worker "+id, *host, *port, handler)
}
