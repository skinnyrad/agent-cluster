# Copilot Instructions for agent-cluster

## Build, Test, and Lint Commands

- **Build:** `go build -o agent-cluster ./cmd/agent-cluster`
- **Test:** `go test -race ./...` (no Ollama needed; tests use `internal/fakeollama`)
- **Lint:** `gofmt -l .` must print nothing; `go vet ./...` must pass
- **Run locally** (Ollama running with `gemma4`):
  ```bash
  ./agent-cluster controller
  ./agent-cluster worker --id worker-1 --port 8001
  ./agent-cluster worker --id worker-2 --port 8002 --tools files,shell
  ./agent-cluster chat
  ```
- **Start a job and follow it:**
  ```bash
  curl -s -X POST localhost:8100/jobs -H 'Content-Type: application/json' -d '{"prompt": "..."}'
  curl -N localhost:8100/jobs/<job_id>/events
  ```

## High-Level Architecture

A distributed agent cluster on local Ollama models, in one Go binary with `controller`, `worker` and `chat` subcommands.

- **Controller** (`internal/controller`): each request is a job. A router agent plans subtasks with `depends_on` and `requires` using Ollama structured output; `normalizePlan` cleans the plan; the scheduler (`pool.go`) runs each subtask on a live worker with a free slot once its dependencies finish; a reviewer agent may add rounds; a synthesizer agent streams the answer. Progress is published as server-sent events and stored in SQLite (`store.go`).
- **Worker** (`internal/worker`): runs one model. `/run` executes the agent loop from `internal/agent` with the task's system prompt and the worker's tools, enforces `--slots`, and streams newline-delimited JSON events ending in a `result`. Workers register with the controller on a heartbeat.
- **Tools** (`internal/tools`): `files` (workspace-confined), `shell` (`run_command`, not a sandbox), `web` (`fetch_url`).
- **Shared types** (`internal/schemas`): registration, tasks, results, subtasks, jobs and events.

### Scheduling rules
- Only live workers (heartbeat within `--heartbeat-ttl`) that have every `requires` capability (tag, tool name or `model:<id>`) and a free slot are eligible.
- A subtask with `worker_id` waits for that worker while it is alive, because workspaces are per worker.
- Otherwise prefer the worker that ran all of the subtask's dependencies, then the least loaded.
- Connection errors and non-2xx responses mark a worker dead and retry the subtask once elsewhere. Timeouts and cancellations do not mark workers dead.

## Key Conventions

- `cmd/agent-cluster` only parses flags and environment variables; logic belongs in `internal/`.
- Change `internal/schemas` first when changing anything on the wire; JSON stays snake_case.
- Thread `context.Context` through network and model calls so job cancellation stops work.
- Every flag has an environment variable fallback; name it in the flag's help text in brackets.
- Services default to `127.0.0.1` and must refuse wider exposure without a token (`httputil.CheckExposure`).
- Add an in-process test in `internal/controller/controller_test.go` for scheduling or job behaviour changes.

## References
- See `Architecture.md` for the job lifecycle, event types and security model.
