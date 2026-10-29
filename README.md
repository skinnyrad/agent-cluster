# agent-cluster

Agent-cluster framework used in the AI For TSCM 2 course. It provides a dynamic agent router where a central controller (backed by an LLM) decomposes tasks on the fly, assigns roles to workers at runtime, and synthesizes their results into a final answer.

## Overview

`agent-cluster` runs one controller service and any number of generic worker services, all written in Go and backed by local models served by [Ollama](https://ollama.com/):

- Each **worker** is role-agnostic — it receives a dynamic `system_prompt` per request that defines its role for that task, then makes a fresh call to its Ollama model.
- The **controller** is itself an LLM-driven router. It reads the available worker pool from `workers.yaml`, uses a **router agent** to decompose the user's prompt into per-worker subtasks (each with a custom system prompt and task prompt), dispatches them over HTTP, and uses a **synthesizer agent** to merge the results into a final answer.
- Shared types in `internal/schemas` define the JSON request/response contracts used by the controller, workers and chat client.

Workers have no fixed roles — the controller assigns roles dynamically at dispatch time based on what the task requires and how many workers are available.

## Project layout

```text
agent-cluster/
├── go.mod
├── workers.yaml
├── cmd/
│   ├── controller/      # controller entry point
│   ├── worker/          # worker entry point
│   └── chat/            # interactive terminal chat client
└── internal/
    ├── agent/           # minimal Ollama-backed LLM agent
    ├── controller/      # router → dispatch → synthesizer pipeline, health polling
    ├── worker/          # /health and /run handlers
    ├── registry/        # workers.yaml loader
    ├── schemas/         # shared JSON contracts
    └── httputil/        # small HTTP/env helpers
```

For detailed architecture and design decisions, see `Architecture.md`.

## Getting started

1. **Install prerequisites**

   - Go 1.26 or newer
   - Ollama, running, with the models you plan to use (for example `ollama pull gemma4`)

2. **Build** (optional — `go run` works too)

   ```bash
   go build -o bin/ ./cmd/...
   ```

3. **Configure workers**

   Edit `workers.yaml` to list your workers with `worker_id`, `url`, `model_id`, `enabled`, and `tags`. No `role` needed.

   The default fleet runs both enabled workers on `gemma4`, so Ollama keeps a single model loaded and can serve them in parallel. A `granite4.2` worker is listed but disabled: on a 32 GB machine it can't stay loaded next to `gemma4`, and the two models end up swapping instead of running together.

4. **Run workers** (start as many as you want — they are all generic)

   ```bash
   WORKER_ID=worker-1 MODEL_ID=gemma4 go run ./cmd/worker --port 8001
   ```
   ```bash
   WORKER_ID=worker-2 MODEL_ID=gemma4 go run ./cmd/worker --port 8002
   ```

5. **Run the controller** (from the repository root, so it finds `workers.yaml`)

   ```bash
   ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 go run ./cmd/controller --port 8100
   ```

Every service takes `--host` (default `0.0.0.0`) and `--port`. The controller also takes `--workers` to point at a different registry file, and `WORKER_TIMEOUT` sets how many seconds each worker gets per subtask (default 300). A worker that times out fails that subtask but stays in the pool. Set `OLLAMA_HOST` if Ollama isn't on `http://localhost:11434`.

## Example usage

- Check a worker:

  ```bash
  curl http://localhost:8001/health
  ```

- List available workers:

  ```bash
  curl http://localhost:8100/workers
  ```

- **Interactive chat (recommended):** Start a conversation with the fleet using the built-in terminal client:

  ```bash
  go run ./cmd/chat
  # or point at a non-default controller:
  go run ./cmd/chat --url http://localhost:8100
  # or via environment variable:
  CONTROLLER_URL=http://localhost:8100 go run ./cmd/chat
  ```

  The chat client supports:
  - **↑ / ↓** arrows to cycle through your previous inputs
  - **← / →** arrows to move the cursor within the current line
  - **6-turn rolling context** — the last 6 user+assistant exchanges are included automatically as conversation history on each dispatch
  - **Verbose status** — a live spinner cycles through *Tasking fleet → Router decomposing → Dispatching to workers → Waiting on fleet results* while the request is in flight
  - **Slash commands** — `/workers`, `/clear`, `/help`, `/quit`

  The client waits up to 15 minutes for each dispatch; change that with `--timeout <seconds>` or `CHAT_TIMEOUT`.

  Markdown is rendered with a dark-background style. On a light terminal, run it with `GLAMOUR_STYLE=light`.

- Dispatch a task directly (the controller decides how many workers to use and what each one does):

  ```bash
  curl -X POST http://localhost:8100/dispatch \
    -H "Content-Type: application/json" \
    -d '{"prompt": "Do a code review and write tests for a Python bubble sort function."}'
  ```

  Response shape:
  ```json
  {
    "request_id": "...",
    "original_prompt": "...",
    "subtasks": [
      {"worker_id": "worker-1", "system_prompt": "You are a code reviewer...", "task_prompt": "..."},
      {"worker_id": "worker-2", "system_prompt": "You are a test engineer...", "task_prompt": "..."}
    ],
    "results": [...],
    "synthesis": "Final synthesized answer..."
  }
  ```

### Chat slash commands

| Command | Description |
|---|---|
| `/workers` | Display a live status table of all registered workers |
| `/clear` | Clear the rolling conversation history |
| `/help` | Show available slash commands |
| `/quit` | Exit the chat client |

## Testing

```bash
go test ./...
```

The controller tests start real workers and a fake Ollama server in-process, so they run without Ollama or any models installed.

## Documentation

- Architecture details and design decisions live in `Architecture.md`.
