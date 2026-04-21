## Agent-Cluster Architecture

## References

- https://docs.agno.com
- https://docs.agno.com/reference/agents/agent
- https://docs.agno.com/agents/running-agents
- https://fastapi.tiangolo.com/deployment/manually/
- https://fastapi.tiangolo.com/deployment/server-workers/
- https://pydantic.dev/docs/validation/latest/concepts/models/
- https://www.python-httpx.org/api/

## Architecture Overview

This project uses a dynamic agent router architecture in which one controller process orchestrates any number of generic remote worker agents over HTTP. The controller is itself LLM-driven: it uses a **router Agent** to decompose incoming tasks and assign roles on the fly, dispatches subtasks to workers over HTTP, and uses a **synthesizer Agent** to merge results into a coherent final answer. Workers are fully stateless and role-agnostic — they receive a `system_prompt` per request that defines their role for that task.

The system has three main layers: shared contracts, generic worker services, and a two-stage controller pipeline. Shared contracts define typed request and response models with Pydantic so both sides validate the same payloads. Worker services expose `/health` and `/run`, and create a fresh `Agent` for each `/run` request using the provided `system_prompt`. The controller's router Agent reads the available worker pool and the user prompt, then outputs a JSON plan assigning each worker a specific `system_prompt` and `task_prompt`. After all workers respond, the synthesizer Agent produces the final answer.

A typical request flow:
1. `POST /dispatch` receives `{"prompt": "..."}`.
2. The **router Agent** reads the worker pool from `workers.yaml` and outputs a JSON subtask plan: `[{worker_id, system_prompt, task_prompt}, ...]`.
3. The controller dispatches each subtask to its assigned worker via `POST /run`.
4. Each worker creates a per-request `Agent(instructions=[system_prompt])` and calls `agent.run(task_prompt)`.
5. The **synthesizer Agent** merges all worker results into a final answer.
6. `DispatchResponse` returns `request_id`, `subtasks`, `results`, and `synthesis`.

If the router Agent returns unparseable JSON, the controller falls back to broadcasting the original prompt to all workers with a generic system prompt.

Key design decisions:
- Workers have no fixed roles — the controller LLM assigns roles dynamically based on the task.
- Workers create a new `Agent` per request so the dynamic `system_prompt` fully takes effect with no state bleed between tasks.
- The controller is stateless. Sessions and user IDs are forwarded to workers so Agno can preserve per-user context when needed.
- Worker-to-worker communication is avoided; all coordination flows through the controller, keeping execution visible and deterministic.
- Scaling means adding more worker instances to `workers.yaml` — no schema or controller changes needed.

Project layout:

```text
agent-cluster/
├── requirements.txt
├── workers.yaml
├── schemas.py
├── worker.py
└── controller.py
```

## File roles

- `requirements.txt` — runtime dependencies for controller and workers.
- `workers.yaml` — fleet registry with worker IDs, URLs, model IDs, enabled flags, and tags. No `role` field.
- `schemas.py` — shared Pydantic models: `AgentTask`, `AgentResult`, `HealthResponse`, `WorkerInfo`, `SubTask`, `DispatchRequest`, `DispatchResponse`.
- `worker.py` — generic FastAPI app exposing `/health` and `/run`. Creates a per-request `Agent` using `task.system_prompt`.
- `controller.py` — FastAPI app with router Agent (task decomposition) and synthesizer Agent (result merging).

## Shared models (`schemas.py`)

| Model | Purpose |
|---|---|
| `AgentTask` | Sent to workers: `task_id`, `system_prompt`, `prompt`, `context`, `session_id`, `user_id` |
| `AgentResult` | Returned by workers: `task_id`, `worker_id`, `role`, `success`, `content`, `metrics`, `error` |
| `HealthResponse` | Worker health: `ok`, `worker_id`, `status`, `model_id` |
| `WorkerInfo` | Registry entry: `worker_id`, `url`, `model_id`, `enabled`, `tags` |
| `SubTask` | Router plan item: `worker_id`, `system_prompt`, `task_prompt` |
| `DispatchRequest` | Controller input: `prompt`, `context`, `session_id`, `user_id` |
| `DispatchResponse` | Controller output: `request_id`, `original_prompt`, `subtasks`, `results`, `synthesis` |

## Environment variables

**Workers:**
- `WORKER_ID` — unique identifier (must match `workers.yaml` entry)
- `MODEL_ID` — Ollama model to use (default: `gemma4`)
- `WORKER_STATUS` — initial status reported in health checks (default: `idle`)

**Controller:**
- `ROUTER_MODEL_ID` — Ollama model for the router Agent (default: `gemma4`)
- `SYNTHESIZER_MODEL_ID` — Ollama model for the synthesizer Agent (default: `gemma4`)
- `HEALTH_POLL_INTERVAL` — seconds between background worker health probes (default: `30`)

## workers.yaml format

`workers.yaml` is the fleet registry. The controller reads it at each request (no restart needed to add/remove workers). Each entry must match the `WorkerInfo` schema.

Required fields:
- `worker_id` — unique identifier for the worker instance.
- `url` — base HTTP URL where the worker API is running, e.g. `http://localhost:8001`.

Optional fields:
- `model_id` — Ollama model the worker is running; shown to the router Agent so it can make informed assignments.
- `enabled` — boolean; set to `false` to exclude a worker from dispatch without removing it.
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

A practical rule: every entry in `workers.yaml` should correspond to a running `uvicorn worker:app` process whose `WORKER_ID` env var matches the `worker_id` in the file and whose port matches the `url`.
