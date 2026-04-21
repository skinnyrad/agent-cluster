# GEMINI.md - Project Context: agent-cluster

## Project Overview
`agent-cluster` is a dynamic agent orchestration framework designed for the "AI For TSCM 2" course. It implements a **Controller-Worker** architecture where a central controller uses Large Language Models (LLMs) to decompose complex tasks into subtasks, assign them to a fleet of generic, role-agnostic workers, and synthesize their outputs into a final response.

### Core Technologies
- **Language:** Python 3.10+
- **Agent Framework:** [Agno](https://docs.agno.com) (formerly Phidata)
- **Web Framework:** [FastAPI](https://fastapi.tiangolo.com/) with Uvicorn
- **LLM Provider:** [Ollama](https://ollama.com/) (local models like `gemma4` or `llama3.1`)
- **Data Validation:** [Pydantic v2](https://pydantic.dev/)
- **Communication:** [HTTPX](https://www.python-httpx.org/) (Asynchronous HTTP)
- **Configuration:** YAML (`workers.yaml`)

### Architecture
1.  **Controller (`controller.py`):**
    - **Router Agent:** Decomposes user prompts into specific `SubTask` objects (JSON).
    - **Dispatcher:** Sends subtasks to available workers concurrently.
    - **Synthesizer Agent:** Merges worker outputs into a final coherent answer.
    - **Health Monitoring:** Background task that polls workers every `HEALTH_POLL_INTERVAL` (default: 30s) to maintain a list of live nodes.
2.  **Worker (`worker.py`):**
    - **Role-Agnostic:** Does not have a fixed persona.
    - **Dynamic Execution:** Receives a `system_prompt` and `task_prompt` per request, creating a fresh Agno `Agent` for each task to prevent state bleed.
3.  **Shared Schemas (`schemas.py`):**
    - Defines the protocol for `AgentTask`, `AgentResult`, `WorkerInfo`, etc., ensuring type safety across the network.

---

## Building and Running

### 1. Prerequisites
- Python 3.10+
- Ollama installed and running with the required models (e.g., `ollama pull gemma4`).

### 2. Setup
```bash
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
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
WORKER_ID=worker-1 MODEL_ID=gemma4 uvicorn worker:app --host 0.0.0.0 --port 8001

# Worker 2
WORKER_ID=worker-2 MODEL_ID=gemma4 uvicorn worker:app --host 0.0.0.0 --port 8002
```

**Start Controller:**
```bash
ROUTER_MODEL_ID=gemma4 SYNTHESIZER_MODEL_ID=gemma4 uvicorn controller:app --host 0.0.0.0 --port 8000
```

---

## Development Conventions

### Coding Style
- **Type Hinting:** Strictly use Python type hints for all function signatures and Pydantic models.
- **Asynchronous I/O:** Use `async/await` for all network calls (FastAPI endpoints, HTTPX client).
- **Schema-First:** Update `schemas.py` before changing the communication logic between controller and workers.

### Testing and Validation
- **Health Checks:** Use `GET /health` on workers and `GET /workers` on the controller to verify system state.
- **Dispatch Testing:** Test the full pipeline using `POST /dispatch`:
  ```bash
  curl -X POST http://localhost:8000/dispatch \
    -H "Content-Type: application/json" \
    -d '{"prompt": "Your complex task here"}'
  ```

### Key Environment Variables
| Variable | Default | Description |
|---|---|---|
| `WORKER_ID` | `worker-1` | Unique ID for the worker instance. |
| `MODEL_ID` | `gemma4` | Ollama model ID for the worker. |
| `ROUTER_MODEL_ID` | `gemma4` | Ollama model for task decomposition. |
| `SYNTHESIZER_MODEL_ID` | `gemma4` | Ollama model for result merging. |
| `HEALTH_POLL_INTERVAL`| `30` | Seconds between worker health checks. |

---

## Key Files
- `controller.py`: Main orchestration logic and LLM pipeline.
- `worker.py`: Generic execution node.
- `schemas.py`: Shared Pydantic models.
- `workers.yaml`: Fleet registration.
- `Architecture.md`: Detailed design documentation.
