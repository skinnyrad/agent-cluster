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

This project uses a distributed controller/worker architecture in which one controller process orchestrates any number of remote worker agents over HTTP, while each worker runs its own local Agno `Agent` instance and exposes a small API for health checks and task execution. The design avoids Agno Teams and AgentOS by moving coordination into plain Python services, which makes each worker independently deployable, replaceable, and reachable across the network.

The system has three main layers: shared contracts, worker services, and a controller service. Shared contracts define typed request and response models with Pydantic so both sides validate the same task payloads and result schemas, worker services wrap specialized Agno agents behind endpoints like `/health` and `/run`, and the controller uses `httpx.AsyncClient` to fan out tasks to selected workers concurrently and aggregate their results.

A typical request flow starts when a user or upstream system sends a top-level task to the controller. The controller classifies the job, selects workers by capability such as code generation, review, testing, or documentation, dispatches subtasks over HTTP in parallel, receives normalized worker outputs, and returns either a combined result set or a synthesized final response. Each worker remains responsible for its own prompts, tools, model choice, and Agno execution loop via `Agent.run()` or `Agent.arun()`, while the controller remains responsible for routing, retries, timeouts, and result aggregation.

The recommended file structure is intentionally small so the system stays easy to reason about and test. A minimal layout is: `controller.py` for orchestration and fleet routing, `worker.py` for one networked worker service template, `schemas.py` for shared Pydantic models, `workers.yaml` for fleet definitions, `.env` for model and host configuration, and optional `workers/` submodules if different worker roles need separate prompt/tool bundles. This structure keeps network contracts stable while allowing each worker role to evolve independently.

Key design decisions are: keep Agno inside workers instead of trying to distribute Agno internals directly, use HTTP as the transport boundary because it is easy to debug and deploy, and make the controller stateless except for in-memory routing and aggregation state. Sessions and user identifiers should still be forwarded to workers so Agno can preserve context per worker when needed, but worker-to-worker communication should be avoided in the first version so all coordination stays visible and deterministic through the controller. This produces a simple “fleet” model where scaling usually means adding more worker instances or roles rather than redesigning orchestration.

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
- `workers.yaml` — simple fleet registry with worker IDs, roles, URLs, and enabled flags.
- `schemas.py` — shared Pydantic models like `AgentTask`, `AgentResult`, `HealthResponse`, and `WorkerInfo`.
- `worker.py` — one FastAPI app that wraps a local Agno agent and exposes `/health` and `/run`.
- `controller.py` — one FastAPI app that loads the registry and sends tasks to matching workers over HTTP.

### workers.yaml format

`workers.yaml` is the fleet registry file for the controller. It defines which workers exist, what role each worker serves, where that worker is reachable over HTTP, and whether the worker is enabled for dispatch. The file uses a top-level `workers` key whose value is a YAML sequence of worker entries, because YAML represents structured configuration as mappings and sequences, which makes it a natural fit for a simple service registry.

Each worker entry should match the shape expected by the shared `WorkerInfo` schema in `schemas.py`. At minimum, each worker should define `worker_id`, `role`, and `url`; it may also include `enabled` and `tags`. The `role` value must match one of the allowed worker roles defined in the shared schema so the registry can be validated before the controller attempts dispatch.

Recommended fields:

- `worker_id` — unique identifier for the worker instance.
- `role` — routing capability such as `general`, `code`, `review`, `test`, or `docs`.
- `url` — base HTTP URL where the worker API is running, such as `http://localhost:8002`.
- `enabled` — boolean flag that allows a worker to remain in the file without receiving tasks.
- `tags` — optional list of descriptive labels for filtering, debugging, or future capability matching.

Example:

```yaml
workers:
  - worker_id: general-1
    role: general
    url: http://localhost:8001
    enabled: true
    tags:
      - default
      - general

  - worker_id: code-1
    role: code
    url: http://localhost:8002
    enabled: true
    tags:
      - python
      - implementation

  - worker_id: review-1
    role: review
    url: http://localhost:8003
    enabled: true
    tags:
      - critique
      - quality
```

In this design, the controller reads `workers.yaml`, validates each entry into a typed worker object, filters out disabled workers, and selects matching workers by `role` before sending HTTP requests with `httpx.AsyncClient`. That keeps the registry simple, human-editable, and easy to evolve without changing the controller’s network contract.

A practical rule is that every entry in `workers.yaml` should correspond to a real running worker process whose environment variables match the same `worker_id`, `role`, and port represented by its `url`. This keeps the registry, the runtime process, and the worker API responses aligned so routing and debugging stay predictable.## Agent-Cluster Architecture

## References

- https://docs.agno.com
- https://docs.agno.com/reference/agents/agent
- https://docs.agno.com/agents/running-agents
- https://fastapi.tiangolo.com/deployment/manually/
- https://fastapi.tiangolo.com/deployment/server-workers/
- https://pydantic.dev/docs/validation/latest/concepts/models/
- https://www.python-httpx.org/api/

## Architecture Overview

This project uses a distributed controller/worker architecture in which one controller process orchestrates any number of remote worker agents over HTTP, while each worker runs its own local Agno `Agent` instance and exposes a small API for health checks and task execution. The design avoids Agno Teams and AgentOS by moving coordination into plain Python services, which makes each worker independently deployable, replaceable, and reachable across the network.

The system has three main layers: shared contracts, worker services, and a controller service. Shared contracts define typed request and response models with Pydantic so both sides validate the same task payloads and result schemas, worker services wrap specialized Agno agents behind endpoints like `/health` and `/run`, and the controller uses `httpx.AsyncClient` to fan out tasks to selected workers concurrently and aggregate their results.

A typical request flow starts when a user or upstream system sends a top-level task to the controller. The controller classifies the job, selects workers by capability such as code generation, review, testing, or documentation, dispatches subtasks over HTTP in parallel, receives normalized worker outputs, and returns either a combined result set or a synthesized final response. Each worker remains responsible for its own prompts, tools, model choice, and Agno execution loop via `Agent.run()` or `Agent.arun()`, while the controller remains responsible for routing, retries, timeouts, and result aggregation.

The recommended file structure is intentionally small so the system stays easy to reason about and test. A minimal layout is: `controller.py` for orchestration and fleet routing, `worker.py` for one networked worker service template, `schemas.py` for shared Pydantic models, `workers.yaml` for fleet definitions, `.env` for model and host configuration, and optional `workers/` submodules if different worker roles need separate prompt/tool bundles. This structure keeps network contracts stable while allowing each worker role to evolve independently.

Key design decisions are: keep Agno inside workers instead of trying to distribute Agno internals directly, use HTTP as the transport boundary because it is easy to debug and deploy, and make the controller stateless except for in-memory routing and aggregation state. Sessions and user identifiers should still be forwarded to workers so Agno can preserve context per worker when needed, but worker-to-worker communication should be avoided in the first version so all coordination stays visible and deterministic through the controller. This produces a simple “fleet” model where scaling usually means adding more worker instances or roles rather than redesigning orchestration.

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
- `workers.yaml` — simple fleet registry with worker IDs, roles, URLs, and enabled flags.
- `schemas.py` — shared Pydantic models like `AgentTask`, `AgentResult`, `HealthResponse`, and `WorkerInfo`.
- `worker.py` — one FastAPI app that wraps a local Agno agent and exposes `/health` and `/run`.
- `controller.py` — one FastAPI app that loads the registry and sends tasks to matching workers over HTTP.

### workers.yaml format

`workers.yaml` is the fleet registry file for the controller. It defines which workers exist, what role each worker serves, where that worker is reachable over HTTP, and whether the worker is enabled for dispatch. The file uses a top-level `workers` key whose value is a YAML sequence of worker entries, because YAML represents structured configuration as mappings and sequences, which makes it a natural fit for a simple service registry.

Each worker entry should match the shape expected by the shared `WorkerInfo` schema in `schemas.py`. At minimum, each worker should define `worker_id`, `role`, and `url`; it may also include `enabled` and `tags`. The `role` value must match one of the allowed worker roles defined in the shared schema so the registry can be validated before the controller attempts dispatch.

Recommended fields:

- `worker_id` — unique identifier for the worker instance.
- `role` — routing capability such as `general`, `code`, `review`, `test`, or `docs`.
- `url` — base HTTP URL where the worker API is running, such as `http://localhost:8002`.
- `enabled` — boolean flag that allows a worker to remain in the file without receiving tasks.
- `tags` — optional list of descriptive labels for filtering, debugging, or future capability matching.

Example:

```yaml
workers:
  - worker_id: general-1
    role: general
    url: http://localhost:8001
    enabled: true
    tags:
      - default
      - general

  - worker_id: code-1
    role: code
    url: http://localhost:8002
    enabled: true
    tags:
      - python
      - implementation

  - worker_id: review-1
    role: review
    url: http://localhost:8003
    enabled: true
    tags:
      - critique
      - quality
```

In this design, the controller reads `workers.yaml`, validates each entry into a typed worker object, filters out disabled workers, and selects matching workers by `role` before sending HTTP requests with `httpx.AsyncClient`. That keeps the registry simple, human-editable, and easy to evolve without changing the controller’s network contract.

A practical rule is that every entry in `workers.yaml` should correspond to a real running worker process whose environment variables match the same `worker_id`, `role`, and port represented by its `url`. This keeps the registry, the runtime process, and the worker API responses aligned so routing and debugging stay predictable.