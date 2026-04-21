from __future__ import annotations

import asyncio
import json
import os
import uuid
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Dict, List

import httpx
import yaml
from fastapi import FastAPI, HTTPException
from agno.agent import Agent
from agno.models.ollama import Ollama

from schemas import AgentTask, AgentResult, HealthResponse, WorkerInfo, SubTask, DispatchRequest, DispatchResponse

BASE_DIR = Path(__file__).resolve().parent
REGISTRY_PATH = BASE_DIR / "workers.yaml"

ROUTER_MODEL_ID = os.getenv("ROUTER_MODEL_ID", "gemma4")
SYNTHESIZER_MODEL_ID = os.getenv("SYNTHESIZER_MODEL_ID", "gemma4")
HEALTH_POLL_INTERVAL = int(os.getenv("HEALTH_POLL_INTERVAL", "30"))

# worker_id -> alive; replaced atomically on each refresh
_live_workers: Dict[str, bool] = {}


def load_workers() -> List[WorkerInfo]:
    if not REGISTRY_PATH.exists():
        return []
    data = yaml.safe_load(REGISTRY_PATH.read_text()) or {}
    workers = data.get("workers", [])
    return [WorkerInfo.model_validate(worker) for worker in workers if worker.get("enabled", True)]


def get_available_workers() -> List[WorkerInfo]:
    """Return enabled workers that passed their last health probe."""
    return [w for w in load_workers() if _live_workers.get(w.worker_id, False)]


async def probe_worker(client: httpx.AsyncClient, worker: WorkerInfo) -> bool:
    """Return True only if the worker is reachable, reports ok=True, and its worker_id matches."""
    try:
        r = await client.get(f"{worker.url}/health", timeout=5.0)
        if r.status_code != 200:
            return False
        data = HealthResponse.model_validate(r.json())
        return data.ok and data.worker_id == worker.worker_id and data.status != "offline"
    except Exception:
        return False


async def refresh_worker_status() -> None:
    """Probe all enabled workers concurrently and atomically replace _live_workers."""
    global _live_workers
    workers = load_workers()
    async with httpx.AsyncClient() as client:
        results = await asyncio.gather(*(probe_worker(client, w) for w in workers))
    _live_workers = {w.worker_id: alive for w, alive in zip(workers, results)}


async def _background_health_poll() -> None:
    while True:
        await asyncio.sleep(HEALTH_POLL_INTERVAL)
        try:
            await refresh_worker_status()
        except Exception:
            pass  # never let a refresh failure kill the loop


@asynccontextmanager
async def lifespan(app: FastAPI):
    await refresh_worker_status()
    poll_task = asyncio.create_task(_background_health_poll())
    try:
        yield
    finally:
        poll_task.cancel()
        try:
            await poll_task
        except asyncio.CancelledError:
            pass


app = FastAPI(title="Agno Fleet Controller", lifespan=lifespan)


def parse_router_plan(text: str, workers: List[WorkerInfo]) -> List[SubTask]:
    """Parse router agent JSON output into SubTasks. Falls back to broadcasting on failure."""
    try:
        start = text.find("[")
        end = text.rfind("]") + 1
        if start != -1 and end > start:
            raw = json.loads(text[start:end])
            worker_ids = {w.worker_id for w in workers}
            subtasks = []
            for item in raw:
                wid = item.get("worker_id", "")
                if wid not in worker_ids:
                    wid = workers[0].worker_id
                subtasks.append(SubTask(
                    worker_id=wid,
                    system_prompt=item.get("system_prompt", "You are a helpful assistant."),
                    task_prompt=item.get("task_prompt", ""),
                ))
            if subtasks:
                return subtasks
    except Exception:
        pass

    # Fallback: broadcast original prompt to all workers with a generic system prompt
    return [
        SubTask(
            worker_id=w.worker_id,
            system_prompt="You are a helpful assistant. Complete the assigned task concisely.",
            task_prompt="",
        )
        for w in workers
    ]


@app.get("/workers")
def list_workers():
    workers = load_workers()
    return {
        "workers": [
            {**worker.model_dump(), "alive": _live_workers.get(worker.worker_id, False)}
            for worker in workers
        ]
    }


@app.post("/dispatch", response_model=DispatchResponse)
async def dispatch(request: DispatchRequest):
    workers = get_available_workers()
    if not workers:
        raise HTTPException(status_code=503, detail="No live workers available")

    request_id = str(uuid.uuid4())

    # Router agent: decompose the user prompt into per-worker subtasks
    worker_descriptions = "\n".join(
        f"- worker_id: {w.worker_id}, model: {w.model_id or 'unknown'}, tags: {w.tags}"
        for w in workers
    )

    router_agent = Agent(
        model=Ollama(id=ROUTER_MODEL_ID),
        instructions=[
            "You are a task router for a distributed agent system.",
            "Given a user task and a list of available worker agents, decompose the task into focused subtasks.",
            "Assign each subtask to a specific worker. Use as many or as few workers as the task requires.",
            "For each subtask, write a system_prompt that defines that worker's role/persona, and a task_prompt with the specific work to do.",
            "You MUST respond with ONLY a valid JSON array and nothing else — no explanation, no markdown fences.",
            'Format: [{"worker_id": "...", "system_prompt": "...", "task_prompt": "..."}, ...]',
        ],
        markdown=False,
    )

    router_prompt = (
        f"Task: {request.prompt}\n\n"
        f"Available workers:\n{worker_descriptions}\n\n"
        "Decompose this task into subtasks. Return JSON only."
    )

    router_response = router_agent.run(router_prompt)
    subtasks = parse_router_plan(str(router_response.content), workers)

    # Fill any empty task_prompts (fallback case) with the original prompt
    for st in subtasks:
        if not st.task_prompt:
            st.task_prompt = request.prompt

    worker_map = {w.worker_id: w for w in workers}

    # Dispatch each subtask to its assigned worker
    async with httpx.AsyncClient(timeout=120.0) as client:
        results: List[AgentResult] = []
        for subtask in subtasks:
            worker = worker_map.get(subtask.worker_id)
            if not worker:
                results.append(AgentResult(
                    task_id=request_id,
                    worker_id=subtask.worker_id,
                    role="",
                    success=False,
                    content="",
                    metrics={},
                    error=f"Worker '{subtask.worker_id}' not found in registry",
                ))
                continue

            task = AgentTask(
                task_id=str(uuid.uuid4()),
                system_prompt=subtask.system_prompt,
                prompt=subtask.task_prompt,
                context=request.context,
                session_id=request.session_id,
                user_id=request.user_id,
            )
            try:
                response = await client.post(f"{worker.url}/run", json=task.model_dump())
                response.raise_for_status()
                results.append(AgentResult.model_validate(response.json()))
            except Exception as exc:
                # Mark worker dead immediately so it's excluded from the next dispatch
                _live_workers[worker.worker_id] = False
                results.append(AgentResult(
                    task_id=task.task_id,
                    worker_id=worker.worker_id,
                    role="",
                    success=False,
                    content="",
                    metrics={},
                    error=str(exc),
                ))

    # Synthesizer agent: merge all worker results into a final answer
    synthesizer_agent = Agent(
        model=Ollama(id=SYNTHESIZER_MODEL_ID),
        instructions=[
            "You are a synthesis agent.",
            "You receive the results from multiple specialist worker agents and combine them into a single coherent final answer.",
            "Be concise and structured. Directly address the original task.",
        ],
        markdown=True,
    )

    results_text = "\n\n".join(
        f"### Worker {r.worker_id}\n{r.content if r.success else f'ERROR: {r.error}'}"
        for r in results
    )
    synthesis_response = synthesizer_agent.run(
        f"Original task: {request.prompt}\n\nWorker results:\n{results_text}\n\nSynthesize a final answer."
    )

    return DispatchResponse(
        request_id=request_id,
        original_prompt=request.prompt,
        subtasks=subtasks,
        results=results,
        synthesis=str(synthesis_response.content),
    )
