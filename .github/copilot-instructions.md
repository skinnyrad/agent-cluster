# Copilot Instructions for agent-cluster

## Build, Test, and Lint Commands

- **Install dependencies:**
  ```bash
  python3 -m venv venv
  source ./venv/bin/activate
  pip install -r requirements.txt
  ```
- **Run a worker** (no role needed — workers are generic):
  ```bash
  WORKER_ID=worker-1 MODEL_ID=gemma4 \
    uvicorn worker:app --host 0.0.0.0 --port 8001
  ```
- **Run the controller:**
  ```bash
  ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 \
    uvicorn controller:app --host 0.0.0.0 --port 8000
  ```
- **Check a worker:**
  ```bash
  curl http://localhost:8001/health
  ```
- **List workers from the controller:**
  ```bash
  curl http://localhost:8000/workers
  ```
- **Dispatch a task:**
  ```bash
  curl -X POST http://localhost:8000/dispatch \
    -H "Content-Type: application/json" \
    -d '{"prompt": "Do a code review and write tests for a Python bubble sort function."}'
  ```

## High-Level Architecture

This project implements a **dynamic agent router** pattern:

- **Controller** (`controller.py`): A two-stage LLM pipeline. First, a **router Agent** (Agno + Ollama) receives the user's prompt and the available worker pool, then decomposes the task into per-worker subtasks — each with a dynamically generated `system_prompt` and `task_prompt`. Second, a **synthesizer Agent** merges all worker results into a final answer.
- **Worker** (`worker.py`): Fully generic. Each `/run` request receives a `system_prompt` from the controller and creates a fresh `Agent` with those instructions before executing the task. Workers have no fixed role.
- **Shared Models** (`schemas.py`): Pydantic models for request/response contracts — `AgentTask` (includes `system_prompt`), `AgentResult`, `WorkerInfo` (no role), `SubTask`, `DispatchRequest`, `DispatchResponse`.
- **Fleet Registry** (`workers.yaml`): Lists workers by `worker_id`, `url`, `model_id`, `enabled`, and `tags`. No `role` field — the controller assigns roles dynamically at dispatch time.

### Worker health tracking
The controller probes every enabled worker's `GET /health` at startup (before serving any requests) and again on a background timer (`HEALTH_POLL_INTERVAL`, default 30 s). Only workers that return `ok: true` with a matching `worker_id` are considered live. The `/dispatch` endpoint only routes tasks to live workers. If a dispatch call to a worker fails mid-flight, that worker is immediately marked dead and excluded from subsequent requests until the next successful health probe.

`GET /workers` returns each worker's current `alive` status alongside its registry metadata.

### Request flow
1. **Startup** — controller probes all enabled workers and builds the initial live-worker set.
2. `POST /dispatch` receives `{"prompt": "..."}`.
3. Controller's **router Agent** receives only the currently live workers and outputs a JSON plan: `[{worker_id, system_prompt, task_prompt}, ...]`.
4. Controller dispatches each subtask to its assigned worker via `POST /run`, with the dynamic `system_prompt` embedded in the `AgentTask`.
5. Workers create a per-request `Agent` using the provided `system_prompt` and execute the task.
6. If a worker fails mid-dispatch it is immediately marked dead.
7. Controller's **synthesizer Agent** combines all results into a final answer.
8. `DispatchResponse` returns `request_id`, `subtasks` plan, raw `results`, and `synthesis`.

## Key Conventions

- **No `WorkerRole` literal** — roles are free-form strings assigned by the router LLM at runtime.
- **`workers.yaml` has no `role` field** — each entry needs `worker_id`, `url`, `model_id`, `enabled`, `tags`.
- **Workers are stateless per request** — a new `Agent` is created per `/run` call using `task.system_prompt`.
- **Environment variables:**
  - Workers: `WORKER_ID`, `MODEL_ID`, `WORKER_STATUS`
  - Controller: `ROUTER_MODEL_ID`, `SYNTHESIZER_MODEL_ID`, `HEALTH_POLL_INTERVAL` (default: `30`)
- **Router fallback:** If the router agent returns unparseable JSON, the controller broadcasts the original prompt to all workers with a generic system prompt.
- **Extending the worker pool:** Add entries to `workers.yaml` and start the corresponding `uvicorn worker:app` processes. No schema changes needed.

## References
- See `Architecture.md` for detailed design decisions.
- Agno, FastAPI, Pydantic, and HTTPX usage follow their official documentation.
