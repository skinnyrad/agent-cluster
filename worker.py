from __future__ import annotations

import os
from fastapi import FastAPI
from agno.agent import Agent
from agno.models.ollama import Ollama

from schemas import AgentResult, AgentTask, HealthResponse

WORKER_ID = os.getenv("WORKER_ID", "worker-1")
MODEL_ID = os.getenv("MODEL_ID", "gemma4")
WORKER_STATUS = os.getenv("WORKER_STATUS", "idle")

app = FastAPI(title=f"Agno Worker - {WORKER_ID}")


@app.get("/health", response_model=HealthResponse)
def health() -> HealthResponse:
    return HealthResponse(
        ok=True,
        worker_id=WORKER_ID,
        status=WORKER_STATUS,
        model_id=MODEL_ID,
    )


@app.post("/run", response_model=AgentResult)
def run_task(task: AgentTask) -> AgentResult:
    try:
        agent = Agent(
            model=Ollama(id=MODEL_ID),
            instructions=[task.system_prompt],
            markdown=True,
            add_datetime_to_context=True,
        )
        response = agent.run(
            task.prompt,
            user_id=task.user_id,
            session_id=task.session_id,
        )
        metrics = getattr(response, "metrics", {}) or {}
        if not isinstance(metrics, dict):
            try:
                metrics = metrics.model_dump()
            except Exception:
                try:
                    metrics = dict(metrics)
                except Exception:
                    metrics = {}
        return AgentResult(
            task_id=task.task_id,
            worker_id=WORKER_ID,
            role="",
            success=True,
            content=str(response.content),
            metrics=metrics,
            error=None,
        )
    except Exception as exc:
        return AgentResult(
            task_id=task.task_id,
            worker_id=WORKER_ID,
            role="",
            success=False,
            content="",
            metrics={},
            error=str(exc),
        )
