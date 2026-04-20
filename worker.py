from __future__ import annotations

import os
from fastapi import FastAPI
from agno.agent import Agent
from agno.models.ollama import Ollama

from schemas import AgentResult, AgentTask, HealthResponse

WORKER_ID = os.getenv("WORKER_ID", "worker-1")
WORKER_ROLE = os.getenv("WORKER_ROLE", "general")
MODEL_ID = os.getenv("MODEL_ID", "llama3.1:8b")
WORKER_STATUS = os.getenv("WORKER_STATUS", "idle")

agent = Agent(
    model=Ollama(id=MODEL_ID),
    description=f"{WORKER_ROLE} specialist worker",
    instructions=[
        f"You are worker {WORKER_ID}.",
        f"Your specialty is {WORKER_ROLE}.",
        "Return concise and useful results.",
    ],
    markdown=True,
    add_datetime_to_context=True,
    add_history_to_context=True,
)

app = FastAPI(title=f"Agno Worker - {WORKER_ID}")


@app.get("/health", response_model=HealthResponse)
def health() -> HealthResponse:
    return HealthResponse(
        ok=True,
        worker_id=WORKER_ID,
        role=WORKER_ROLE,
        status=WORKER_STATUS,
        model_id=MODEL_ID,
    )


@app.post("/run", response_model=AgentResult)
def run_task(task: AgentTask) -> AgentResult:
    try:
        response = agent.run(
            task.prompt,
            user_id=task.user_id,
            session_id=task.session_id,
        )
        return AgentResult(
            task_id=task.task_id,
            worker_id=WORKER_ID,
            role=WORKER_ROLE,
            success=True,
            content=str(response.content),
            metrics=getattr(response, "metrics", {}) or {},
            error=None,
        )
    except Exception as exc:
        return AgentResult(
            task_id=task.task_id,
            worker_id=WORKER_ID,
            role=WORKER_ROLE,
            success=False,
            content="",
            metrics={},
            error=str(exc),
        )
