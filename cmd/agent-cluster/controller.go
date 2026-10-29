package main

import (
	"log"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/controller"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
)

func runController(args []string) error {
	fs := newFlags("controller", "Coordinate jobs across the workers that register with this controller.")
	host := fs.String("host", httputil.Getenv("CONTROLLER_HOST", "127.0.0.1"), "interface to listen on; use 0.0.0.0 for other machines (needs a token) [CONTROLLER_HOST]")
	port := fs.Int("port", httputil.GetenvInt("CONTROLLER_PORT", 8100), "port to listen on [CONTROLLER_PORT]")
	token := fs.String("token", httputil.Getenv("CLUSTER_TOKEN", ""), "shared secret every node must present [CLUSTER_TOKEN]")
	insecure := fs.Bool("insecure", httputil.GetenvBool("CLUSTER_INSECURE"), "allow listening beyond localhost without a token [CLUSTER_INSECURE]")
	db := fs.String("db", httputil.Getenv("AGENT_CLUSTER_DB", "agent-cluster.db"), "SQLite file for jobs and events [AGENT_CLUSTER_DB]")
	workers := fs.String("workers", httputil.Getenv("WORKERS_FILE", "workers.yaml"), "optional worker allowlist; missing file allows any worker [WORKERS_FILE]")
	router := fs.String("router-model", httputil.Getenv("ROUTER_MODEL_ID", "gemma4"), "Ollama model for planning and review [ROUTER_MODEL_ID]")
	synth := fs.String("synthesizer-model", httputil.Getenv("SYNTHESIZER_MODEL_ID", "gemma4"), "Ollama model for the final answer [SYNTHESIZER_MODEL_ID]")
	numCtx := fs.Int("num-ctx", httputil.GetenvInt("NUM_CTX", 16384), "context window for the controller's models, in tokens [NUM_CTX]")
	numPredict := fs.Int("num-predict", httputil.GetenvInt("NUM_PREDICT", 4096), "maximum tokens per reply from the controller's models [NUM_PREDICT]")
	workerTimeout := fs.Int("worker-timeout", httputil.GetenvInt("WORKER_TIMEOUT", int(controller.DefaultWorkerTimeout/time.Second)), "seconds a worker gets per subtask [WORKER_TIMEOUT]")
	ttl := fs.Int("heartbeat-ttl", httputil.GetenvInt("HEARTBEAT_TTL", int(controller.DefaultHeartbeatTTL/time.Second)), "seconds without a heartbeat before a worker is considered gone [HEARTBEAT_TTL]")
	rounds := fs.Int("max-rounds", httputil.GetenvInt("MAX_ROUNDS", controller.DefaultMaxRounds), "rounds of work per job; 1 disables the reviewer [MAX_ROUNDS]")
	history := fs.Int("history-turns", httputil.GetenvInt("HISTORY_TURNS", controller.DefaultHistoryTurns), "earlier exchanges in a conversation shown to the router; 0 for none [HISTORY_TURNS]")
	fs.Parse(args)

	if err := httputil.CheckExposure(*host, *token, *insecure); err != nil {
		return err
	}
	if *workerTimeout <= 0 || *ttl <= 0 || *rounds <= 0 {
		log.Fatal("--worker-timeout, --heartbeat-ttl and --max-rounds must be positive")
	}
	if *history <= 0 {
		*history = -1 // the controller treats zero as "use the default"
	}

	ctl, err := controller.New(controller.Config{
		Token:            *token,
		StorePath:        *db,
		AllowlistPath:    *workers,
		RouterModel:      *router,
		SynthesizerModel: *synth,
		Options:          &agent.Options{NumCtx: *numCtx, NumPredict: *numPredict},
		WorkerTimeout:    time.Duration(*workerTimeout) * time.Second,
		HeartbeatTTL:     time.Duration(*ttl) * time.Second,
		MaxRounds:        *rounds,
		HistoryTurns:     *history,
	})
	if err != nil {
		return err
	}
	defer ctl.Close()

	ctx, stop := signalContext()
	defer stop()
	return httputil.Serve(ctx, "controller", *host, *port, ctl.Handler())
}
