## Agent-Cluster Architecture

## Architecture Overview

This project uses a dynamic agent router architecture in which one controller process orchestrates any number of generic remote worker agents over HTTP. The controller is itself LLM-driven: it uses a **router agent** to decompose incoming tasks and assign roles on the fly, dispatches subtasks to workers over HTTP, and uses a **synthesizer agent** to merge results into a coherent final answer. Workers are fully stateless and role-agnostic — they receive a `system_prompt` per request that defines their role for that task.

The system has three main layers: shared contracts, generic worker services, and a two-stage controller pipeline. Shared contracts (`internal/schemas`) define the JSON request and response types so every component reads and writes the same payloads. Worker services expose `/health` and `/run`, and make a fresh model call for each `/run` request using the provided `system_prompt`. The controller's router agent reads the available worker pool and the user prompt, then outputs a JSON plan assigning each worker a specific `system_prompt` and `task_prompt`. After all workers respond, the synthesizer agent produces the final answer.

A typical request flow:
1. **Startup** — the controller probes every enabled worker's `GET /health` and builds the initial live-worker set.
2. `POST /dispatch` receives `{"prompt": "..."}`.
3. The **router agent** is shown only the currently live workers from `workers.yaml` and outputs a JSON subtask plan: `[{worker_id, system_prompt, task_prompt}, ...]`.
4. The controller sends each subtask to its assigned worker via `POST /run`, all at once; results are returned in plan order.
5. Each worker sends `system_prompt` as the system message and `task_prompt` as the user message to its Ollama model.
6. If a call to a worker fails, that worker is immediately marked dead.
7. The **synthesizer agent** merges all worker results into a final answer.
8. `DispatchResponse` returns `request_id`, `subtasks`, `results`, and `synthesis`.

Dispatch is concurrent, but when several workers share one Ollama server, Ollama decides how many requests actually run at once (see its `OLLAMA_NUM_PARALLEL` and `OLLAMA_MAX_LOADED_MODELS` settings, and available memory). For real parallelism across different models, run workers against separate Ollama hosts with `OLLAMA_HOST`.

If the router agent returns unparseable JSON (or an empty plan), the controller falls back to broadcasting the original prompt to all live workers with a generic system prompt. A subtask that names an unknown worker is reassigned to the first live worker.

Key design decisions:
- Workers have no fixed roles — the controller LLM assigns roles dynamically based on the task.
- Workers make a fresh model call per request so the dynamic `system_prompt` fully takes effect with no state bleed between tasks.
- The controller holds no conversation state. `session_id`, `user_id` and `context` are forwarded to workers unchanged.
- Worker-to-worker communication is avoided; all coordination flows through the controller, keeping execution visible and deterministic.
- Scaling means adding more worker instances to `workers.yaml` — no schema or controller changes needed.

## Agents (`internal/agent`)

The Python version used the Agno framework. The Go version replaces the small part of it that was used with a minimal agent that calls Ollama's `POST /api/chat` directly (non-streaming, one system message plus one user message):

- `Instructions` — one instruction is sent as-is; several are sent as a bulleted `<instructions>` block.
- `Markdown` — adds "Use markdown to format your answers." to the system message.
- `AddDatetime` — adds the current time to the system message (workers use this).
- Metrics returned to the controller: `input_tokens`, `output_tokens`, `total_tokens`, `duration` (seconds).

`OLLAMA_HOST` selects the Ollama server (default `http://localhost:11434`).

## Worker health tracking

The controller probes every enabled worker's `GET /health` at startup (before serving any requests) and again on a background timer (`HEALTH_POLL_INTERVAL`, default 30 s), probing all workers concurrently. Only workers that answer `200` with `ok: true`, a status other than `offline`, and a `worker_id` matching the registry are considered live. `/dispatch` only routes tasks to live workers. If a dispatch call to a worker fails mid-flight (connection error, timeout, or a non-2xx status), that worker is marked dead until the next successful probe.

`GET /workers` returns each enabled worker's registry metadata with its current `alive` status.

## Chat client (`cmd/chat`)

`cmd/chat` is the recommended way to interact with the fleet during development. It wraps `POST /dispatch` in an interactive terminal session.

**Input handling** — built on [`liner`](https://github.com/peterh/liner):
- ↑ / ↓ arrows cycle through the input history for the current session.
- ← / → arrows and the standard readline editing shortcuts work within the current line.

**Conversation context** — a rolling window of the last 12 messages (6 full user+assistant turns). Before each dispatch call, these are serialised into a `[Conversation so far]` preamble prepended to the current prompt so the router agent has multi-turn context.

**Verbose status** — a spinner goroutine cycles through stage labels while the request is in flight:
1. *Tasking fleet…*
2. *Router decomposing task…*
3. *Dispatching to workers…*
4. *Waiting on fleet results…*
5. (then a rotating set of light-hearted wait messages every 20 s)
6. ✓ Done! (request_id: …)

After the response arrives, the client prints the subtask plan (worker ID, role preview, task preview), per-worker result panels (green on success, red on error), and the final synthesis rendered as Markdown with [Glamour](https://github.com/charmbracelet/glamour). Tables and panels use [Lip Gloss](https://github.com/charmbracelet/lipgloss).

**Slash commands:**

| Command | Action |
|---|---|
| `/workers` | Fetch `GET /workers` and render a status table |
| `/clear` | Clear the rolling conversation history |
| `/help` | Show available slash commands |
| `/quit` | Exit (`/exit` and `/q` also work) |

## Project layout

```text
agent-cluster/
├── go.mod
├── workers.yaml
├── cmd/
│   ├── controller/main.go
│   ├── worker/main.go
│   └── chat/{main,client,display,status}.go
└── internal/
    ├── agent/
    ├── controller/
    ├── worker/
    ├── registry/
    ├── schemas/
    └── httputil/
```

## Package roles

- `workers.yaml` — fleet registry with worker IDs, URLs, model IDs, enabled flags, and tags. No `role` field.
- `cmd/worker` — reads env vars and flags, then serves `internal/worker`.
- `cmd/controller` — reads env vars and flags, then serves `internal/controller`.
- `cmd/chat` — interactive terminal chat client.
- `internal/worker` — `/health` and `/run` handlers. Runs a per-request agent using `task.system_prompt`.
- `internal/controller` — router agent (task decomposition, `plan.go`), concurrent dispatch, synthesizer agent (result merging), and health polling.
- `internal/agent` — minimal Ollama chat client and agent.
- `internal/registry` — loads enabled workers from `workers.yaml`. The file is re-read on every request, so edits take effect without a restart.
- `internal/schemas` — shared JSON types.
- `internal/httputil` — JSON request/response helpers, env defaults, `--host`/`--port` flags, request logging.

## Shared types (`internal/schemas`)

| Type | Purpose |
|---|---|
| `AgentTask` | Sent to workers: `task_id`, `system_prompt`, `prompt`, `context`, `session_id`, `user_id` |
| `AgentResult` | Returned by workers: `task_id`, `worker_id`, `role`, `success`, `content`, `metrics`, `error` |
| `HealthResponse` | Worker health: `ok`, `worker_id`, `status` (`idle`, `busy` or `offline`), `model_id` |
| `WorkerInfo` | Registry entry: `worker_id`, `url`, `model_id`, `enabled`, `tags` |
| `SubTask` | Router plan item: `worker_id`, `system_prompt`, `task_prompt` |
| `DispatchRequest` | Controller input: `prompt`, `context`, `session_id`, `user_id` |
| `DispatchResponse` | Controller output: `request_id`, `original_prompt`, `subtasks`, `results`, `synthesis` |

Invalid request bodies get a `422` with a `{"detail": "..."}` body, matching the previous FastAPI behaviour.

## Configuration

All services accept `--host` (default `0.0.0.0`) and `--port`.

**Workers** (`--port` default `8001`):
- `WORKER_ID` — unique identifier (must match the `workers.yaml` entry; default `worker-1`)
- `MODEL_ID` — Ollama model to use (default: `gemma4`)
- `WORKER_STATUS` — status reported in health checks (default: `idle`)

**Controller** (`--port` default `8100`):
- `ROUTER_MODEL_ID` — Ollama model for the router agent (default: `gemma4`)
- `SYNTHESIZER_MODEL_ID` — Ollama model for the synthesizer agent (default: `gemma4`)
- `HEALTH_POLL_INTERVAL` — seconds between background worker health probes (default: `30`)
- `WORKERS_FILE` or `--workers` — registry path (default: `workers.yaml` in the current directory)

**All services:**
- `OLLAMA_HOST` — Ollama server (default: `http://localhost:11434`)

**Chat client:**
- `CONTROLLER_URL` or `--url` — base URL of the controller (default: `http://localhost:8100`)
- `GLAMOUR_STYLE` — Markdown style, e.g. `light` for light terminals (default: `dark`)

## workers.yaml format

`workers.yaml` is the fleet registry. The controller reads it at each request (no restart needed to add/remove workers), though a newly added worker only becomes live after its next health probe.

Required fields:
- `worker_id` — unique identifier for the worker instance.
- `url` — base HTTP URL where the worker API is running, e.g. `http://localhost:8001`.

Optional fields:
- `model_id` — Ollama model the worker is running; shown to the router agent so it can make informed assignments.
- `enabled` — boolean (default `true`); set to `false` to exclude a worker from dispatch without removing it.
- `tags` — descriptive labels for debugging or future capability matching.

Example:

```yaml
workers:
  - worker_id: worker-1
    url: http://localhost:8001
    model_id: gemma4
    enabled: true
    tags:
      - default

  - worker_id: worker-2
    url: http://localhost:8002
    model_id: gemma4
    enabled: true
    tags:
      - default

  - worker_id: worker-3
    url: http://localhost:8003
    model_id: llama3.1:8b
    enabled: true
    tags:
      - large
```

A practical rule: every enabled entry in `workers.yaml` should correspond to a running worker process whose `WORKER_ID` matches the `worker_id` in the file and whose `--port` matches the `url`.

## References

- https://github.com/ollama/ollama/blob/main/docs/api.md
- https://pkg.go.dev/net/http
- https://github.com/peterh/liner
- https://github.com/charmbracelet/lipgloss
- https://github.com/charmbracelet/glamour
