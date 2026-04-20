from __future__ import annotations

import uuid
from pathlib import Path
from typing import List

import httpx
import yaml
from fastapi import FastAPI, HTTPException

from schemas import AgentTask, WorkerInfo

BASE_DIR = Path(__file__).resolve().parent
REGISTRY_PATH = BASE_DIR / "workers.yaml"

app = FastAPI(title="Agno Fleet Controller")


def load_workers() -> List[WorkerInfo]:
    if not REGISTRY_PATH.exists():
        return []
    data = yaml.safe_load(REGISTRY_PATH.read_text()) or {}
    workers = data.get("workers", [])
    return [WorkerInfo.model_validate(worker) for worker in workers if worker.get("enabled", True)]


def select_workers(role: str) -> List[WorkerInfo]:
    workers = load_workers()
    return [worker for worker in workers if worker.role == role]


@app.get("/workers")
def list_workers():
    return {"workers": [worker.model_dump() for worker in load_workers()]}


@app.post("/dispatch")
async def dispatch(payload: dict):
    role = payload.get("role", "general")
    prompt = payload.get("prompt")
    if not prompt:
        raise HTTPException(status_code=400, detail="'prompt' is required")

    workers = select_workers(role)
    if not workers:
        raise HTTPException(status_code=404, detail=f"No enabled workers found for role '{role}'")

    task = AgentTask(
        task_id=str(uuid.uuid4()),
        role=role,
        prompt=prompt,
        context=payload.get("context", {}),
        session_id=payload.get("session_id"),
        user_id=payload.get("user_id"),
    )

    async with httpx.AsyncClient(timeout=120.0) as client:
        results = []
        for worker in workers:
            try:
                response = await client.post(f"{worker.url}/run", json=task.model_dump())
                response.raise_for_status()
                results.append(response.json())
            except Exception as exc:
                results.append(
                    {
                        "task_id": task.task_id,
                        "worker_id": worker.worker_id,
                        "role": worker.role,
                        "success": False,
                        "content": "",
                        "metrics": {},
                        "error": str(exc),
                    }
                )

    return {
        "task": task.model_dump(),
        "worker_count": len(workers),
        "results": results,
    }
