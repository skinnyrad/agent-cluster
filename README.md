# agent-cluster

Agent-cluster framework used for AI For TSCM 2. It provides a lightweight controller/worker model where a central controller orchestrates a fleet of networked Agno agents over HTTP.

## Overview

`agent-cluster` runs one controller service and any number of worker services:

- Each **worker** runs a local Agno `Agent` and exposes a small HTTP API (e.g. `/health`, `/run`) for task execution.
- The **controller** reads a `workers.yaml` registry, selects workers by role (code, review, test, docs, general), and dispatches tasks over HTTP.
- Shared Pydantic models in `schemas.py` define the request/response contracts used by both controller and workers.

This design keeps each worker independently deployable and replaceable, while the controller stays thin and stateless, focused on routing, retries, and result aggregation.

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
- `workers.yaml` — fleet registry with worker IDs, roles, URLs, enabled flags, and tags.
- `schemas.py` — shared Pydantic models (`AgentTask`, `AgentResult`, `HealthResponse`, `WorkerInfo`, etc.).
- `worker.py` — FastAPI app that wraps a local Agno agent and exposes `/health` and `/run`.
- `controller.py` — FastAPI app that loads the registry and sends tasks to matching workers over HTTP.

For detailed architecture, design decisions, and YAML format, see `Architecture.md`.

## Getting started

1. **Install dependencies**

   ```bash
   python3.11 -m venv .venv
   source .venv/bin/activate
   pip install -r requirements.txt
   ```

2. **Configure workers**

   Edit `workers.yaml` to define your workers, their roles, and URLs.

3. **Run a worker**

   ```bash
   WORKER_ID=general-1 WORKER_ROLE=general MODEL_ID=gemma4 \
     uvicorn worker:app --host 0.0.0.0 --port 8001
   ```

4. **Run the controller**

   ```bash
   uvicorn controller:app --host 0.0.0.0 --port 8000
   ```

## Example usage

- Check a worker:

  ```bash
  curl http://localhost:8001/health
  ```

- List workers from the controller:

  ```bash
  curl http://localhost:8000/workers
  ```

- Dispatch a task:

  ```bash
  curl -X POST http://localhost:8000/dispatch \
    -H "Content-Type: application/json" \
    -d '{
      "role": "general",
      "prompt": "Summarize how this controller/worker architecture works."
    }'
  ```

## Documentation

- High-level and low-level architecture details live in `Architecture.md`.
- Agno, FastAPI, Pydantic, and HTTPX usage patterns follow their respective official docs.