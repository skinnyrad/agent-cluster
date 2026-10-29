// Command controller runs the fleet's LLM-driven router.
//
//	ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 go run ./cmd/controller --port 8000
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
	host, port := httputil.ListenFlags(8000)
	workersPath := flag.String("workers", httputil.Getenv("WORKERS_FILE", "workers.yaml"), "path to the worker registry")
	flag.Parse()

	pollSeconds, err := strconv.Atoi(httputil.Getenv("HEALTH_POLL_INTERVAL", "30"))
	if err != nil || pollSeconds <= 0 {
		log.Fatal("HEALTH_POLL_INTERVAL must be a positive number of seconds")
	}

	handler := controller.New(context.Background(), controller.Config{
		WorkersPath:      *workersPath,
		RouterModel:      httputil.Getenv("ROUTER_MODEL_ID", "gemma4"),
		SynthesizerModel: httputil.Getenv("SYNTHESIZER_MODEL_ID", "gemma4"),
		PollInterval:     time.Duration(pollSeconds) * time.Second,
	})
	httputil.ListenAndServe("controller", *host, *port, handler)
}
