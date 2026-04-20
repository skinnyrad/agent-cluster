from __future__ import annotations

from typing import Any, Dict, List, Literal, Optional
from pydantic import BaseModel, Field


WorkerRole = Literal["code", "review", "test", "docs", "general"]
WorkerStatus = Literal["idle", "busy", "offline"]


class AgentTask(BaseModel):
    task_id: str = Field(..., description="Unique task identifier")
    role: WorkerRole = Field(..., description="Requested worker capability")
    prompt: str = Field(..., description="Task prompt sent to the worker agent")
    context: Dict[str, Any] = Field(default_factory=dict, description="Extra task context")
    session_id: Optional[str] = Field(default=None, description="Optional shared session identifier")
    user_id: Optional[str] = Field(default=None, description="Optional end-user identifier")


class AgentResult(BaseModel):
    task_id: str
    worker_id: str
    role: WorkerRole
    success: bool
    content: str = ""
    metrics: Dict[str, Any] = Field(default_factory=dict)
    error: Optional[str] = None


class HealthResponse(BaseModel):
    ok: bool = True
    worker_id: str
    role: WorkerRole
    status: WorkerStatus = "idle"
    model_id: str


class WorkerInfo(BaseModel):
    worker_id: str
    role: WorkerRole
    url: str
    enabled: bool = True
    tags: List[str] = Field(default_factory=list)
