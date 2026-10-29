# GEMINI.md - Project Context: agent-cluster

## Project Overview
`agent-cluster` is a general-purpose distributed agent cluster for local models. A controller turns each request into a job: a router agent plans subtasks with dependencies, a scheduler runs them on registered worker agents across machines, an optional reviewer adds follow-up rounds, and a synthesizer streams the final answer. Workers run Ollama models and can call tools (files, shell, web).

### Core Technologies
- **Language:** Go 1.26+, one binary (`cmd/agent-cluster`) with `controller`, `worker`, `chat` and `version` subcommands
- **LLM Provider:** [Ollama](https://ollama.com/) `/api/chat`, with tool calling, streaming and structured output (`format`)
- **Web Server:** standard library `net/http`; job progress as server-sent events; worker output as newline-delimited JSON
- **Storage:** SQLite through the pure-Go `modernc.org/sqlite`
- **Configuration:** flags with environment variable fallbacks; optional `workers.yaml` allowlist
- **Interactive CLI:** liner (line editing), Lip Gloss (tables, panels), Glamour (Markdown)

### Architecture
- `internal/controller`: HTTP API, jobs and events (`jobs.go`), planner and plan normalization (`plan.go`), scheduler (`pool.go`), store (`store.go`).
- `internal/worker`: `/health` and streaming `/run`, slots, registration heartbeat.
- `internal/agent`: Ollama client and the agent loop (tools, streaming, structured output, model options).
- `internal/tools`: `files`, `shell`, `web` tool sets, confined to a per-worker workspace.
- `internal/chat`: terminal client that follows job events.
- `internal/schemas`: shared JSON types. `internal/registry`: allowlist. `internal/httputil`: serving, auth, helpers.
- `internal/fakeollama`: fake Ollama for tests.

See `Architecture.md` for the job lifecycle, event types, scheduling rules and security model.

---

## Building and Running

```bash
go build -o agent-cluster ./cmd/agent-cluster
./agent-cluster controller
./agent-cluster worker --id worker-1 --port 8001
./agent-cluster worker --id worker-2 --port 8002 --tools files,shell
./agent-cluster chat
```

Prerequisite: Ollama running with a tool-capable model (`ollama pull gemma4`).

Services listen on `127.0.0.1` by default. Listening on other interfaces requires `CLUSTER_TOKEN` (or `--insecure`).

---

## Development Conventions

### Coding Style
- Run `gofmt` and `go vet ./...`; CI fails on either.
- Pass `context.Context` through every network and model call, so cancelling a job stops its work.
- Keep `cmd/agent-cluster` to flag and environment parsing; put logic in `internal/` so it can be tested.
- Update `internal/schemas` first when changing what crosses the wire. JSON field names stay snake_case.
- Every flag has an environment variable fallback, named in its help text in brackets.

### Testing and Validation
- `go test -race ./...` runs unit tests and in-process end-to-end tests (real controller and workers, fake Ollama). Add a test to `internal/controller/controller_test.go` for any scheduling or job behaviour change.
- Script fake model replies with `fakeollama.Server` fields (`RouterReply`, `ReviewReplies`, `ToolModel`, `SlowModel`, `Barrier`).
- For a real run, start the binaries against local Ollama and follow `GET /jobs/{id}/events`, or use `agent-cluster chat`.

### Key Environment Variables
| Variable | Default | Used by |
|---|---|---|
| `CLUSTER_TOKEN` | — | all; required to listen beyond localhost |
| `OLLAMA_HOST` | `http://localhost:11434` | controller, worker |
| `CONTROLLER_URL` | `http://localhost:8100` | worker, chat |
| `CONTROLLER_PORT` / `WORKER_PORT` | `8100` / `8001` | controller / worker |
| `ROUTER_MODEL_ID` / `SYNTHESIZER_MODEL_ID` | `gemma4` | controller |
| `MAX_ROUNDS` | `2` | controller (1 disables the reviewer) |
| `WORKER_TIMEOUT` | `300` | controller, seconds per subtask |
| `AGENT_CLUSTER_DB` | `agent-cluster.db` | controller |
| `WORKERS_FILE` | `workers.yaml` | controller allowlist |
| `WORKER_ID` / `MODEL_ID` | `worker-1` / `gemma4` | worker |
| `WORKER_TOOLS` | `files` | worker (`files,shell,web`) |
| `WORKER_SLOTS` | `1` | worker |
| `NUM_CTX` / `NUM_PREDICT` | `16384` / `4096` | controller, worker |
| `CHAT_TIMEOUT` | `900` | chat |
| `GLAMOUR_STYLE` | `dark` | chat |

---

## Key Files
- `cmd/agent-cluster/*.go`: subcommands and their flags.
- `internal/controller/jobs.go`: the job pipeline and agent prompts.
- `internal/controller/pool.go`: worker registry and scheduling.
- `internal/agent/agent.go`: the tool-calling loop.
- `internal/tools/tools.go`: tool implementations.
- `workers.yaml`: optional worker allowlist.
- `Architecture.md`: design documentation.
