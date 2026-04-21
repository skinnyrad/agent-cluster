#!/usr/bin/env python3
"""Interactive chat CLI for the agent-cluster controller.

Usage:
    python chat.py [--url http://localhost:8000]

Slash commands:
    /workers  — show live worker status table
    /clear    — clear conversation history
    /help     — show available commands
    /quit     — exit
"""
from __future__ import annotations

import argparse
import os
import threading
import time
from collections import deque
from typing import Any, Deque, Dict, List, Optional

import httpx
from prompt_toolkit import PromptSession
from prompt_toolkit.history import InMemoryHistory
from rich.columns import Columns
from rich.console import Console
from rich.markdown import Markdown
from rich.panel import Panel
from rich.table import Table
from rich.text import Text

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

DEFAULT_CONTROLLER_URL = os.getenv("CONTROLLER_URL", "http://localhost:8000")

# Each "interaction" = one user message + one assistant message (2 deque entries).
# maxlen=12 keeps the last 6 full interactions.
HISTORY_MAXLEN = 12

# ---------------------------------------------------------------------------
# Globals
# ---------------------------------------------------------------------------

console = Console()


# ---------------------------------------------------------------------------
# Spinner helper
# ---------------------------------------------------------------------------

_SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]


class LiveStatus:
    """A simple threaded spinner that prints status messages to the terminal."""

    def __init__(self) -> None:
        self._message: str = ""
        self._running: bool = False
        self._thread: Optional[threading.Thread] = None
        self._frame_idx: int = 0
        self._lock = threading.Lock()
        self._last_len: int = 0

    def _render(self) -> None:
        while self._running:
            with self._lock:
                frame = _SPINNER_FRAMES[self._frame_idx % len(_SPINNER_FRAMES)]
                self._frame_idx += 1
                line = f"  {frame}  {self._message}"
            # Overwrite the current line
            padded = line.ljust(self._last_len)
            print(f"\r{padded}", end="", flush=True)
            self._last_len = len(line)
            time.sleep(0.08)

    def start(self, message: str) -> None:
        self._message = message
        self._running = True
        self._frame_idx = 0
        self._last_len = 0
        self._thread = threading.Thread(target=self._render, daemon=True)
        self._thread.start()

    def update(self, message: str) -> None:
        with self._lock:
            self._message = message

    def stop(self, final_message: Optional[str] = None) -> None:
        self._running = False
        if self._thread:
            self._thread.join()
        # Clear spinner line
        print(f"\r{' ' * (self._last_len + 4)}\r", end="", flush=True)
        if final_message:
            console.print(final_message)


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------

def fetch_workers(base_url: str) -> Dict[str, Any]:
    with httpx.Client(timeout=10.0) as client:
        resp = client.get(f"{base_url}/workers")
        resp.raise_for_status()
        return resp.json()


def dispatch_prompt(
    base_url: str,
    prompt: str,
    history: Deque[Dict[str, str]],
    status: LiveStatus,
) -> Dict[str, Any]:
    """Send the prompt (with conversation history) to /dispatch and return the response."""

    # Build the augmented prompt that includes prior conversation turns
    augmented_prompt = _build_augmented_prompt(prompt, history)

    status.update("Tasking fleet...")
    console.print()  # blank line before spinner appears

    # Start a background thread that cycles through verbose stage labels
    # while we wait for the blocking HTTP call.
    stage_labels = [
        "Tasking fleet...",
        "Router decomposing task...",
        "Dispatching to workers...",
        "Waiting on fleet results...",
        "Synthesizing response...",
    ]
    stop_cycling = threading.Event()

    def _cycle_stages() -> None:
        # Advance stage label roughly every 3 seconds
        for label in stage_labels[1:]:
            if stop_cycling.wait(3.0):
                return
            status.update(label)

    cycle_thread = threading.Thread(target=_cycle_stages, daemon=True)
    cycle_thread.start()

    with httpx.Client(timeout=300.0) as client:
        payload = {"prompt": augmented_prompt}
        resp = client.post(f"{base_url}/dispatch", json=payload)
        resp.raise_for_status()
        data = resp.json()

    stop_cycling.set()
    cycle_thread.join()
    return data


def _build_augmented_prompt(prompt: str, history: Deque[Dict[str, str]]) -> str:
    if not history:
        return prompt
    lines = ["[Conversation so far]"]
    for msg in history:
        role = "User" if msg["role"] == "user" else "Assistant"
        lines.append(f"{role}: {msg['content']}")
    lines.append("")
    lines.append(f"User: {prompt}")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Display helpers
# ---------------------------------------------------------------------------

def print_workers_table(base_url: str) -> None:
    console.print()
    status = LiveStatus()
    status.start("Fetching worker status...")
    try:
        data = fetch_workers(base_url)
        status.stop()
    except Exception as exc:
        status.stop()
        console.print(f"[bold red]Error fetching workers:[/] {exc}")
        return

    workers: List[Dict[str, Any]] = data.get("workers", [])
    if not workers:
        console.print("[yellow]No workers registered.[/yellow]")
        return

    table = Table(title="Worker Fleet Status", show_lines=True, border_style="blue")
    table.add_column("Worker ID", style="cyan", no_wrap=True)
    table.add_column("URL", style="dim")
    table.add_column("Model", style="magenta")
    table.add_column("Tags", style="dim")
    table.add_column("Alive", justify="center")
    table.add_column("Enabled", justify="center")

    for w in workers:
        alive = w.get("alive", False)
        enabled = w.get("enabled", True)
        alive_str = "[bold green]✓ alive[/]" if alive else "[bold red]✗ dead[/]"
        enabled_str = "[green]yes[/]" if enabled else "[red]no[/]"
        tags = ", ".join(w.get("tags", [])) or "—"
        table.add_row(
            w.get("worker_id", "?"),
            w.get("url", "?"),
            w.get("model_id") or "?",
            tags,
            alive_str,
            enabled_str,
        )

    console.print(table)
    console.print()


def print_dispatch_result(data: Dict[str, Any]) -> None:
    """Print verbose dispatch results then the final synthesis."""
    request_id = data.get("request_id", "?")
    subtasks: List[Dict[str, Any]] = data.get("subtasks", [])
    results: List[Dict[str, Any]] = data.get("results", [])
    synthesis: str = data.get("synthesis", "")

    # ── Subtask plan ────────────────────────────────────────────────────────
    console.print(
        f"\n[bold green]✓ Done![/]  [dim]request_id: {request_id}[/]"
    )
    console.print(
        f"[bold]Created [cyan]{len(subtasks)}[/] subtask(s):[/]"
    )
    for st in subtasks:
        wid = st.get("worker_id", "?")
        tp = st.get("task_prompt", "")
        preview = tp[:80] + ("…" if len(tp) > 80 else "")
        sp = st.get("system_prompt", "")
        sp_preview = sp[:60] + ("…" if len(sp) > 60 else "")
        console.print(f"  [cyan]{wid}[/]  role=[dim]{sp_preview}[/]")
        console.print(f"        task=[italic]{preview}[/]")

    # ── Per-worker results ───────────────────────────────────────────────────
    console.print()
    for r in results:
        wid = r.get("worker_id", "?")
        success = r.get("success", False)
        content = r.get("content", "")
        error = r.get("error")
        metrics = r.get("metrics") or {}

        if success:
            header = Text(f" Worker {wid} — result ", style="bold white on dark_green")
        else:
            header = Text(f" Worker {wid} — ERROR ", style="bold white on dark_red")

        body = error if not success else (content[:400] + ("…" if len(content) > 400 else ""))
        console.print(Panel(body, title=header, border_style="green" if success else "red"))

        if metrics and success:
            metric_items = [
                f"[dim]{k}:[/] {v}" for k, v in list(metrics.items())[:4]
            ]
            console.print("  " + "  ·  ".join(metric_items))

    # ── Synthesis ────────────────────────────────────────────────────────────
    console.print()
    console.rule("[bold blue]Fleet Synthesis[/]")
    if synthesis:
        console.print(Markdown(synthesis))
    else:
        console.print("[yellow](no synthesis returned)[/yellow]")
    console.print()


def print_help() -> None:
    table = Table(show_header=False, box=None, padding=(0, 2))
    table.add_column(style="cyan bold")
    table.add_column(style="dim")
    commands = [
        ("/workers", "Show live worker status table"),
        ("/clear",   "Clear conversation history"),
        ("/help",    "Show this help message"),
        ("/quit",    "Exit the chat"),
    ]
    for cmd, desc in commands:
        table.add_row(cmd, desc)
    console.print(Panel(table, title="[bold]Slash Commands[/]", border_style="blue"))


# ---------------------------------------------------------------------------
# Main chat loop
# ---------------------------------------------------------------------------

def main() -> None:
    parser = argparse.ArgumentParser(description="Interactive chat with the agent-cluster controller")
    parser.add_argument(
        "--url",
        default=DEFAULT_CONTROLLER_URL,
        help=f"Controller base URL (default: {DEFAULT_CONTROLLER_URL})",
    )
    args = parser.parse_args()
    base_url: str = args.url.rstrip("/")

    # Message history for conversation context sent to the controller
    msg_history: Deque[Dict[str, str]] = deque(maxlen=HISTORY_MAXLEN)

    # prompt_toolkit session — provides up/down input recall & left/right cursor
    session: PromptSession = PromptSession(history=InMemoryHistory())

    console.print()
    console.print(Panel(
        "[bold cyan]Agent Cluster Chat[/]\n"
        f"[dim]Controller:[/] [yellow]{base_url}[/]\n"
        "[dim]Type a message to dispatch to the fleet.  "
        "Use [cyan]/help[/] for commands.[/]",
        border_style="cyan",
    ))
    console.print()

    while True:
        try:
            user_input: str = session.prompt("You › ").strip()
        except (EOFError, KeyboardInterrupt):
            console.print("\n[dim]Bye![/]")
            break

        if not user_input:
            continue

        # ── Slash commands ───────────────────────────────────────────────────
        if user_input.startswith("/"):
            cmd = user_input.split()[0].lower()

            if cmd in ("/quit", "/exit", "/q"):
                console.print("[dim]Bye![/]")
                break

            elif cmd == "/clear":
                msg_history.clear()
                console.print("[green]Conversation history cleared.[/green]")
                continue

            elif cmd == "/workers":
                print_workers_table(base_url)
                continue

            elif cmd == "/help":
                print_help()
                continue

            else:
                console.print(f"[yellow]Unknown command:[/] {cmd}  (type [cyan]/help[/] for list)")
                continue

        # ── Dispatch to fleet ────────────────────────────────────────────────
        status = LiveStatus()
        status.start("Tasking fleet...")

        try:
            data = dispatch_prompt(base_url, user_input, msg_history, status)
        except httpx.ConnectError:
            status.stop()
            console.print(f"[bold red]Connection refused.[/] Is the controller running at [yellow]{base_url}[/]?")
            continue
        except httpx.HTTPStatusError as exc:
            status.stop()
            console.print(f"[bold red]HTTP {exc.response.status_code}:[/] {exc.response.text}")
            continue
        except Exception as exc:
            status.stop()
            console.print(f"[bold red]Error:[/] {exc}")
            continue

        status.stop()
        print_dispatch_result(data)

        # Persist this interaction in the rolling history
        synthesis = data.get("synthesis", "")
        if synthesis:
            msg_history.append({"role": "user",      "content": user_input})
            msg_history.append({"role": "assistant", "content": synthesis})


if __name__ == "__main__":
    main()
