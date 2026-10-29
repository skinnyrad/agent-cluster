package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
	"github.com/skinnyrad/agent-cluster/internal/tools"
	"github.com/skinnyrad/agent-cluster/internal/worker"
)

func runWorker(args []string) error {
	fs := newFlags("worker", "Run tasks on one Ollama model and register with the controller.")
	id := fs.String("id", httputil.Getenv("WORKER_ID", "worker-1"), "unique worker ID [WORKER_ID]")
	model := fs.String("model", httputil.Getenv("MODEL_ID", "gemma4"), "Ollama model to run [MODEL_ID]")
	host := fs.String("host", httputil.Getenv("WORKER_HOST", "127.0.0.1"), "interface to listen on; use 0.0.0.0 for other machines (needs a token) [WORKER_HOST]")
	port := fs.Int("port", httputil.GetenvInt("WORKER_PORT", 8001), "port to listen on [WORKER_PORT]")
	controllerURL := fs.String("controller", httputil.Getenv("CONTROLLER_URL", "http://localhost:8100"), "controller to register with; empty to skip registration [CONTROLLER_URL]")
	advertise := fs.String("advertise", httputil.Getenv("WORKER_ADVERTISE_URL", ""), "URL the controller should use to reach this worker (default http://localhost:<port>, or this machine's hostname when listening on all interfaces) [WORKER_ADVERTISE_URL]")
	token := fs.String("token", httputil.Getenv("CLUSTER_TOKEN", ""), "shared secret every node must present [CLUSTER_TOKEN]")
	insecure := fs.Bool("insecure", httputil.GetenvBool("CLUSTER_INSECURE"), "allow listening beyond localhost without a token [CLUSTER_INSECURE]")
	toolSets := fs.String("tools", httputil.Getenv("WORKER_TOOLS", "files"), fmt.Sprintf("comma-separated tool sets to enable (%s); empty for none [WORKER_TOOLS]", strings.Join(tools.Sets, ", ")))
	workspace := fs.String("workspace", httputil.Getenv("WORKER_WORKSPACE", ""), "directory the tools may use (default workspace/<id>) [WORKER_WORKSPACE]")
	commandTimeout := fs.Int("command-timeout", httputil.GetenvInt("COMMAND_TIMEOUT", 60), "seconds before run_command kills a command [COMMAND_TIMEOUT]")
	tags := fs.String("tags", httputil.Getenv("WORKER_TAGS", ""), "comma-separated capability tags, e.g. gpu,code [WORKER_TAGS]")
	slots := fs.Int("slots", httputil.GetenvInt("WORKER_SLOTS", 1), "tasks to run at once; match your Ollama server's OLLAMA_NUM_PARALLEL [WORKER_SLOTS]")
	numCtx := fs.Int("num-ctx", httputil.GetenvInt("NUM_CTX", 16384), "context window, in tokens [NUM_CTX]")
	numPredict := fs.Int("num-predict", httputil.GetenvInt("NUM_PREDICT", 4096), "maximum tokens per model reply [NUM_PREDICT]")
	maxSteps := fs.Int("max-steps", httputil.GetenvInt("MAX_STEPS", 8), "model calls per task when using tools [MAX_STEPS]")
	heartbeat := fs.Int("heartbeat", httputil.GetenvInt("HEARTBEAT_INTERVAL", 5), "seconds between heartbeats to the controller [HEARTBEAT_INTERVAL]")
	fs.Parse(args)

	if err := httputil.CheckExposure(*host, *token, *insecure); err != nil {
		return err
	}

	ws := *workspace
	if ws == "" {
		ws = filepath.Join("workspace", *id)
	}
	toolList, toolNames, err := tools.Build(httputil.SplitList(*toolSets), tools.Config{
		Workspace:      ws,
		CommandTimeout: time.Duration(*commandTimeout) * time.Second,
	})
	if err != nil {
		return err
	}
	if len(toolNames) > 0 {
		abs, _ := filepath.Abs(ws)
		log.Printf("tools: %s (workspace %s)", strings.Join(toolNames, ", "), abs)
	}

	adv := *advertise
	if adv == "" {
		adv = defaultAdvertiseURL(*host, *port)
	}

	wk := worker.New(worker.Config{
		WorkerID:      *id,
		ModelID:       *model,
		Options:       &agent.Options{NumCtx: *numCtx, NumPredict: *numPredict},
		MaxSteps:      *maxSteps,
		Tools:         toolList,
		ToolNames:     toolNames,
		Tags:          httputil.SplitList(*tags),
		Slots:         *slots,
		Token:         *token,
		ControllerURL: strings.TrimRight(*controllerURL, "/"),
		AdvertiseURL:  adv,
		Heartbeat:     time.Duration(*heartbeat) * time.Second,
	})

	ctx, stop := signalContext()
	defer stop()
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		wk.RunHeartbeat(ctx)
	}()
	err = httputil.Serve(ctx, "worker "+*id, *host, *port, wk.Handler())
	stop()
	<-hbDone
	return err
}

// defaultAdvertiseURL guesses how the controller can reach this worker.
func defaultAdvertiseURL(host string, port int) string {
	switch {
	case httputil.IsLoopback(host):
		host = "localhost"
	case host == "" || host == "0.0.0.0" || host == "::":
		name, err := os.Hostname()
		if err != nil {
			name = "localhost"
		}
		host = name
		log.Printf("advertising http://%s:%d to the controller; set --advertise if it can't resolve that name", name, port)
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}
