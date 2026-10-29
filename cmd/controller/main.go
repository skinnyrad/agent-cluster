// Command controller runs the fleet's LLM-driven router.
//
//	ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 go run ./cmd/controller --port 8100
package main

import (
	"context"
	"flag"
	"log"
	"strconv"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/controller"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
)

func main() {
	host, port := httputil.ListenFlags(8100)
	workersPath := flag.String("workers", httputil.Getenv("WORKERS_FILE", "workers.yaml"), "path to the worker registry")
	flag.Parse()

	pollSeconds := positiveSeconds("HEALTH_POLL_INTERVAL", 30)
	workerTimeoutSeconds := positiveSeconds("WORKER_TIMEOUT", int(controller.DefaultWorkerTimeout/time.Second))

	handler := controller.New(context.Background(), controller.Config{
		WorkersPath:      *workersPath,
		RouterModel:      httputil.Getenv("ROUTER_MODEL_ID", "gemma4"),
		SynthesizerModel: httputil.Getenv("SYNTHESIZER_MODEL_ID", "gemma4"),
		PollInterval:     time.Duration(pollSeconds) * time.Second,
		WorkerTimeout:    time.Duration(workerTimeoutSeconds) * time.Second,
	})
	httputil.ListenAndServe("controller", *host, *port, handler)
}

// positiveSeconds reads a whole number of seconds from the environment variable key.
func positiveSeconds(key string, def int) int {
	n, err := strconv.Atoi(httputil.Getenv(key, strconv.Itoa(def)))
	if err != nil || n <= 0 {
		log.Fatalf("%s must be a positive number of seconds", key)
	}
	return n
}
