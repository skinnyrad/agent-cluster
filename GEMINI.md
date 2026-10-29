# GEMINI.md - Project Context: agent-cluster

## Project Overview
`agent-cluster` is a dynamic agent orchestration framework designed for the "AI For TSCM 2" course. It implements a **Controller-Worker** architecture where a central controller uses Large Language Models (LLMs) to decompose complex tasks into subtasks, assign them to a fleet of generic, role-agnostic workers, and synthesize their outputs into a final response.

### Core Technologies
- **Language:** Go 1.26+
- **LLM Provider:** [Ollama](https://ollama.com/) (local models like `gemma4` or `llama3.1`), called directly over its HTTP API
- **Web Server:** Go standard library `net/http`
- **Configuration:** YAML (`workers.yaml`, parsed with `gopkg.in/yaml.v3`)
- **Interactive CLI:** [liner](https://github.com/peterh/liner) (input history, cursor editing) + [Lip Gloss](https://github.com/charmbracelet/lipgloss) (tables, panels) + [Glamour](https://github.com/charmbracelet/glamour) (Markdown rendering)

### Architecture
1.  **Controller (`internal/controller`, run with `cmd/controller`):**
    - **Router Agent:** Decomposes user prompts into `SubTask` objects (JSON).
    - **Dispatcher:** Sends subtasks to live workers concurrently and keeps results in plan order.
    - **Synthesizer Agent:** Merges worker outputs into a final coherent answer.
    - **Health Monitoring:** Background goroutine that polls workers every `HEALTH_POLL_INTERVAL` (default: 30s) to maintain a list of live nodes.
2.  **Worker (`internal/worker`, run with `cmd/worker`):**
    - **Role-Agnostic:** Does not have a fixed persona.
    - **Dynamic Execution:** Receives a `system_prompt` and task prompt per request and makes a fresh model call for each task to prevent state bleed.
3.  **Agent (`internal/agent`):**
    - Minimal Ollama `/api/chat` client: instructions become the system message, the prompt becomes the user message.
4.  **Shared Schemas (`internal/schemas`):**
    - Defines the JSON protocol for `AgentTask`, `AgentResult`, `WorkerInfo`, etc.
5.  **Chat Client (`cmd/chat`):**
    - Interactive terminal session.
    - Maintains a rolling 6-turn conversation history (12 messages) prepended to each dispatch call.
    - Live spinner showing each pipeline stage.
    - Slash commands: `/workers`, `/clear`, `/help`, `/quit`.

---

## Building and Running

### 1. Prerequisites
- Go 1.26+
- Ollama installed and running with the required models (e.g., `ollama pull gemma4`).

### 2. Build
```bash
go build -o bin/ ./cmd/...
```

### 3. Configuration
Define your worker fleet in `workers.yaml`:
```yaml
workers:
  - worker_id: worker-1
    url: http://localhost:8001
    model_id: gemma4
    enabled: true
```

### 4. Running the Services

**Start Workers:**
Run as many workers as defined in `workers.yaml` on their respective ports.
```bash
# Worker 1
WORKER_ID=worker-1 MODEL_ID=gemma4 go run ./cmd/worker --port 8001

# Worker 2
WORKER_ID=worker-2 MODEL_ID=gemma4 go run ./cmd/worker --port 8002
```

**Start Controller** (from the repository root so it finds `workers.yaml`):
```bash
ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 go run ./cmd/controller --port 8100
```

**Start Interactive Chat Client (recommended for interactive use):**
```bash
go run ./cmd/chat
# or override the controller URL:
go run ./cmd/chat --url http://localhost:8100
# or via environment variable:
CONTROLLER_URL=http://localhost:8100 go run ./cmd/chat
```

The chat client prompts you for input and dispatches each message to the fleet. Use ↑/↓ to recall previous inputs, ←/→ to edit the current line. The last 6 conversation turns are automatically included as context in each request.

Slash commands available in the chat client:

| Command | Description |
|---|---|
| `/workers` | Show a live status table of all registered workers |
| `/clear` | Clear rolling conversation history |
| `/help` | Show available slash commands |
| `/quit` | Exit the chat client |

---

## Development Conventions

### Coding Style
- **Formatting:** Run `gofmt` (or `go fmt ./...`) and `go vet ./...` before committing.
- **Context:** Pass `context.Context` through every network call so client disconnects cancel model calls.
- **Schema-First:** Update `internal/schemas` before changing the communication logic between controller and workers. Keep JSON field names snake_case so the wire format stays stable.
- **Thin commands:** Keep `cmd/*` to flag/env parsing; put logic in `internal/*` so it can be tested.

### Testing and Validation
- **Unit and integration tests:** `go test ./...` (the controller tests spin up real workers against a fake Ollama, so no models are needed). Use `go test -race ./...` when touching concurrency.
- **Health Checks:** Use `GET /health` on workers and `GET /workers` on the controller to verify system state.
- **Interactive Testing:** Use `go run ./cmd/chat` for a full interactive session.
- **Direct Dispatch Testing:** Test the full pipeline using `POST /dispatch`:
  ```bash
  curl -X POST http://localhost:8100/dispatch \
    -H "Content-Type: application/json" \
    -d '{"prompt": "Your complex task here"}'
  ```

### Key Environment Variables
| Variable | Default | Description |
|---|---|---|
| `WORKER_ID` | `worker-1` | Unique ID for the worker instance. |
| `MODEL_ID` | `gemma4` | Ollama model ID for the worker. |
| `WORKER_STATUS` | `idle` | Status reported by the worker's `/health`. |
| `ROUTER_MODEL_ID` | `gemma4` | Ollama model for task decomposition. |
| `SYNTHESIZER_MODEL_ID` | `gemma4` | Ollama model for result merging. |
| `HEALTH_POLL_INTERVAL`| `30` | Seconds between worker health checks. |
| `WORKER_TIMEOUT` | `300` | Seconds each worker gets per subtask. Timed-out workers stay live. |
| `WORKERS_FILE` | `workers.yaml` | Registry path used by the controller. |
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama server used by the controller and workers. |
| `CONTROLLER_URL` | `http://localhost:8100` | Controller base URL used by the chat client. |
| `CHAT_TIMEOUT` | `900` | Seconds the chat client waits for each dispatch. |
| `GLAMOUR_STYLE` | `dark` | Markdown style for the chat client (`light` for light terminals). |

---

## Key Files
- `internal/controller/controller.go`: Main orchestration logic and LLM pipeline.
- `internal/controller/plan.go`: Router output parsing and broadcast fallback.
- `internal/worker/worker.go`: Generic execution node.
- `internal/agent/agent.go`: Ollama-backed agent.
- `internal/schemas/schemas.go`: Shared JSON types.
- `workers.yaml`: Fleet registration.
- `cmd/chat/`: Interactive terminal chat client.
- `Architecture.md`: Detailed design documentation.
