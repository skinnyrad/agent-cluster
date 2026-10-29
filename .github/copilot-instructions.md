# Copilot Instructions for agent-cluster

## Build, Test, and Lint Commands

- **Build everything:**
  ```bash
  go build -o bin/ ./cmd/...
  ```
- **Test and vet:**
  ```bash
  go test ./...
  go vet ./...
  ```
- **Run a worker** (no role needed — workers are generic):
  ```bash
  WORKER_ID=worker-1 MODEL_ID=gemma4 go run ./cmd/worker --port 8001
  ```
- **Run the controller** (from the repository root):
  ```bash
  ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 go run ./cmd/controller --port 8100
  ```
- **Run the chat client:**
  ```bash
  go run ./cmd/chat --url http://localhost:8100
  ```
- **Check a worker:**
  ```bash
  curl http://localhost:8001/health
  ```
- **List workers from the controller:**
  ```bash
  curl http://localhost:8100/workers
  ```
- **Dispatch a task:**
  ```bash
  curl -X POST http://localhost:8100/dispatch \
    -H "Content-Type: application/json" \
    -d '{"prompt": "Do a code review and write tests for a Python bubble sort function."}'
  ```

## High-Level Architecture

This project implements a **dynamic agent router** pattern in Go, using Ollama for all model calls:

- **Controller** (`internal/controller`): A two-stage LLM pipeline. First, a **router agent** receives the user's prompt and the live worker pool, then decomposes the task into per-worker subtasks — each with a dynamically generated `system_prompt` and `task_prompt`. Second, a **synthesizer agent** merges all worker results into a final answer.
- **Worker** (`internal/worker`): Fully generic. Each `/run` request receives a `system_prompt` from the controller and makes a fresh model call with those instructions. Workers have no fixed role.
- **Agent** (`internal/agent`): Minimal Ollama `/api/chat` client used by both controller and workers.
- **Shared types** (`internal/schemas`): JSON contracts — `AgentTask` (includes `system_prompt`), `AgentResult`, `HealthResponse`, `WorkerInfo` (no role), `SubTask`, `DispatchRequest`, `DispatchResponse`.
- **Fleet Registry** (`workers.yaml`, loaded by `internal/registry`): Lists workers by `worker_id`, `url`, `model_id`, `enabled`, and `tags`. No `role` field — the controller assigns roles dynamically at dispatch time.
- **Entry points** (`cmd/controller`, `cmd/worker`, `cmd/chat`): Only parse flags and environment variables; logic lives in `internal/`.

### Worker health tracking
The controller probes every enabled worker's `GET /health` at startup (before serving any requests) and again on a background timer (`HEALTH_POLL_INTERVAL`, default 30 s). Only workers that return `ok: true`, a non-`offline` status, and a matching `worker_id` are considered live. The `/dispatch` endpoint only routes tasks to live workers. If a dispatch call to a worker fails mid-flight, that worker is immediately marked dead and excluded from subsequent requests until the next successful health probe.

`GET /workers` returns each worker's current `alive` status alongside its registry metadata.

### Request flow
1. **Startup** — controller probes all enabled workers and builds the initial live-worker set.
2. `POST /dispatch` receives `{"prompt": "..."}`.
3. Controller's **router agent** receives only the currently live workers and outputs a JSON plan: `[{worker_id, system_prompt, task_prompt}, ...]`.
4. Controller dispatches each subtask to its assigned worker via `POST /run`, with the dynamic `system_prompt` embedded in the `AgentTask`.
5. Workers call their Ollama model with the provided `system_prompt` and execute the task.
6. If a worker fails mid-dispatch it is immediately marked dead.
7. Controller's **synthesizer agent** combines all results into a final answer.
8. `DispatchResponse` returns `request_id`, `subtasks` plan, raw `results`, and `synthesis`.

## Key Conventions

- **No role field** — roles are free-form strings assigned by the router LLM at runtime; `workers.yaml` entries need `worker_id`, `url`, `model_id`, `enabled`, `tags`.
- **Workers are stateless per request** — a new agent is built per `/run` call using `task.system_prompt`.
- **JSON field names stay snake_case** — the wire format is shared by every component.
- **Pass `context.Context`** through network calls so a dropped client cancels in-flight model calls.
- **Environment variables:**
  - Workers: `WORKER_ID`, `MODEL_ID`, `WORKER_STATUS`
  - Controller: `ROUTER_MODEL_ID`, `SYNTHESIZER_MODEL_ID`, `HEALTH_POLL_INTERVAL` (default: `30`), `WORKERS_FILE`
  - All services: `OLLAMA_HOST`
  - Chat: `CONTROLLER_URL`, `GLAMOUR_STYLE`
- **Router fallback:** If the router agent returns unparseable JSON, the controller broadcasts the original prompt to all workers with a generic system prompt.
- **Extending the worker pool:** Add entries to `workers.yaml` and start the corresponding `cmd/worker` processes. No code changes needed.

## References
- See `Architecture.md` for detailed design decisions.
