from __future__ import annotations

from typing import Any, Dict, List, Literal, Optional
from pydantic import BaseModel, Field


WorkerStatus = Literal["idle", "busy", "offline"]


class AgentTask(BaseModel):
    task_id: str = Field(..., description="Unique task identifier")
    system_prompt: str = Field(..., description="Dynamic role/instructions assigned to this worker for the task")
    prompt: str = Field(..., description="Task prompt sent to the worker agent")
    context: Dict[str, Any] = Field(default_factory=dict, description="Extra task context")
    session_id: Optional[str] = Field(default=None, description="Optional shared session identifier")
    user_id: Optional[str] = Field(default=None, description="Optional end-user identifier")


class AgentResult(BaseModel):
    task_id: str
    worker_id: str
    role: str = ""
    success: bool
    content: str = ""
    metrics: Dict[str, Any] = Field(default_factory=dict)
    error: Optional[str] = None


class HealthResponse(BaseModel):
    ok: bool = True
    worker_id: str
    status: WorkerStatus = "idle"
    model_id: str


class WorkerInfo(BaseModel):
    worker_id: str
    url: str
    model_id: Optional[str] = None
    enabled: bool = True
    tags: List[str] = Field(default_factory=list)


class SubTask(BaseModel):
    worker_id: str = Field(..., description="ID of the worker assigned this subtask")
    system_prompt: str = Field(..., description="Dynamic role/instructions for this worker")
    task_prompt: str = Field(..., description="Specific task prompt for this worker")


class DispatchRequest(BaseModel):
    prompt: str = Field(..., description="High-level task prompt")
    context: Dict[str, Any] = Field(default_factory=dict)
    session_id: Optional[str] = None
    user_id: Optional[str] = None


class DispatchResponse(BaseModel):
    request_id: str
    original_prompt: str
    subtasks: List[SubTask]
    results: List[AgentResult]
    synthesis: str
