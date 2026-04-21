# agent-cluster

Agent-cluster framework used in the AI For TSCM 2 course. It provides a dynamic agent router where a central controller (backed by an LLM) decomposes tasks on the fly, assigns roles to workers at runtime, and synthesizes their results into a final answer.

## Overview

`agent-cluster` runs one controller service and any number of generic worker services:

- Each **worker** is role-agnostic — it receives a dynamic `system_prompt` per request that defines its role for that task, then creates a fresh Agno `Agent` and executes the work.
- The **controller** is itself an LLM-driven router. It reads the available worker pool from `workers.yaml`, uses a **router Agent** to decompose the user's prompt into per-worker subtasks (each with a custom system prompt and task prompt), dispatches them over HTTP, and uses a **synthesizer Agent** to merge the results into a final answer.
- Shared Pydantic models in `schemas.py` define the request/response contracts used by both controller and workers.

Workers have no fixed roles — the controller assigns roles dynamically at dispatch time based on what the task requires and how many workers are available.

## Project layout

```text
agent-cluster/
├── requirements.txt
├── workers.yaml
├── schemas.py
├── worker.py
└── controller.py
```

- `requirements.txt` — runtime dependencies for controller and workers.
- `workers.yaml` — fleet registry with worker IDs, URLs, model IDs, enabled flags, and tags. No `role` field.
- `schemas.py` — shared Pydantic models (`AgentTask`, `AgentResult`, `WorkerInfo`, `SubTask`, `DispatchRequest`, `DispatchResponse`, etc.).
- `worker.py` — FastAPI app exposing `/health` and `/run`. Creates a per-request `Agent` using the provided `system_prompt`.
- `controller.py` — FastAPI app with a two-stage LLM pipeline: router Agent (task decomposition) + synthesizer Agent (result merging).

For detailed architecture and design decisions, see `Architecture.md`.

## Getting started

1. **Install dependencies**

   ```bash
   python3 -m venv venv
   source ./venv/bin/activate
   pip install -r requirements.txt
   ```

2. **Configure workers**

   Edit `workers.yaml` to list your workers with `worker_id`, `url`, `model_id`, `enabled`, and `tags`. No `role` needed.

3. **Run workers** (start as many as you want — they are all generic)

   ```bash
   WORKER_ID=worker-1 MODEL_ID=gemma4 \
     uvicorn worker:app --host 0.0.0.0 --port 8001
   ```
   ```
   WORKER_ID=worker-2 MODEL_ID=gemma4 \
     uvicorn worker:app --host 0.0.0.0 --port 8002
   ```

4. **Run the controller**

   ```bash
   ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 \
     uvicorn controller:app --host 0.0.0.0 --port 8000
   ```

## Example usage

- Check a worker:

  ```bash
  curl http://localhost:8001/health
  ```

- List available workers:

  ```bash
  curl http://localhost:8000/workers
  ```

- Dispatch a task (the controller decides how many workers to use and what each one does):

  ```bash
  curl -X POST http://localhost:8000/dispatch \
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

## Documentation

- Architecture details and design decisions live in `Architecture.md`.
- Agno, FastAPI, Pydantic, and HTTPX usage patterns follow their respective official docs.