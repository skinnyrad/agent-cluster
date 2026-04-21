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

### Request flow
1. `POST /dispatch` receives `{"prompt": "..."}`.
2. Controller's **router Agent** reads the available workers and outputs a JSON plan: `[{worker_id, system_prompt, task_prompt}, ...]`.
3. Controller dispatches each subtask to its assigned worker via `POST /run`, with the dynamic `system_prompt` embedded in the `AgentTask`.
4. Workers create a per-request `Agent` using the provided `system_prompt` and execute the task.
5. Controller's **synthesizer Agent** combines all results into a final answer.
6. `DispatchResponse` returns `request_id`, `subtasks` plan, raw `results`, and `synthesis`.

## Key Conventions

- **No `WorkerRole` literal** — roles are free-form strings assigned by the router LLM at runtime.
- **`workers.yaml` has no `role` field** — each entry needs `worker_id`, `url`, `model_id`, `enabled`, `tags`.
- **Workers are stateless per request** — a new `Agent` is created per `/run` call using `task.system_prompt`.
- **Environment variables:**
  - Workers: `WORKER_ID`, `MODEL_ID`, `WORKER_STATUS`
  - Controller: `ROUTER_MODEL_ID`, `SYNTHESIZER_MODEL_ID`
- **Router fallback:** If the router agent returns unparseable JSON, the controller broadcasts the original prompt to all workers with a generic system prompt.
- **Extending the worker pool:** Add entries to `workers.yaml` and start the corresponding `uvicorn worker:app` processes. No schema changes needed.

## References
- See `Architecture.md` for detailed design decisions.
- Agno, FastAPI, Pydantic, and HTTPX usage follow their official documentation.
