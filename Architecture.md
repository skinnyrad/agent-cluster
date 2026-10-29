## Agent-Cluster Architecture

agent-cluster has one controller and any number of workers. Workers run agents on local Ollama models and register themselves with the controller. The controller turns each request into a job: it plans subtasks, schedules them across workers, optionally reviews the results, and synthesizes an answer. All coordination flows through the controller; workers never talk to each other.

## Job lifecycle

1. **Create.** `POST /jobs` stores a job (status `running`) and returns its ID. The work runs in the background, so clients follow it with `GET /jobs/{id}/events`. `POST /dispatch` does the same and waits for the end.
2. **Plan.** The router agent sees the request, the conversation history for the session, and the live workers with their model, slots, tags and tools. It replies in a JSON schema enforced by Ollama's `format` option:

   ```json
   {"subtasks": [{"id": "t1", "title": "...", "system_prompt": "...", "task_prompt": "...",
                  "depends_on": [], "requires": [], "worker_id": ""}]}
   ```

   If the reply can't be parsed (some models ignore `format`), the whole request becomes a single subtask.
3. **Normalize.** IDs are made unique (follow-up rounds use `r2-t1` and so on). Dependencies on unknown IDs are dropped. Requirements that no live worker can meet are dropped. If the dependencies form a cycle, all of them are dropped.
4. **Schedule.** Each subtask starts as soon as its dependencies have finished. Their results are appended to its task prompt. The scheduler picks a worker:
   - only live workers with every capability in `requires` (tags, tool names, or `model:<id>`) and a free slot;
   - a subtask with a `worker_id` waits for that worker while it is alive, because it may need files in that worker's workspace;
   - otherwise the worker that ran all of its dependencies, if it's free;
   - otherwise the least-loaded worker.

   When every eligible worker is busy, the subtask waits for a slot.
5. **Run.** The worker runs the task and streams events back. If the worker can't be reached or returns an error status, it is marked dead until its next heartbeat and the subtask is retried once on another worker. A timeout (`--worker-timeout`) fails only that subtask; the worker stays live, since slow is not the same as down.
6. **Review.** With `--max-rounds` above 1, a reviewer agent sees the history, the task and every result, and replies `{"complete", "reason", "subtasks"}`. Follow-up subtasks become the next round and may depend on earlier results. An unreadable review counts as complete.
7. **Synthesize.** The synthesizer agent merges every result into the final answer and streams it.
8. **Finish.** The job ends `done`, `failed` or `cancelled` (`DELETE /jobs/{id}`, or the client leaving a `/dispatch` call).

## Events

`GET /jobs/{id}/events` is a server-sent event stream. Each `data:` line is a JSON `schemas.Event`:

| Type | Carries |
|---|---|
| `job.started` | — |
| `plan` | `round`, `subtasks` |
| `subtask.started` | `subtask_id`, `worker_id` |
| `subtask.token` | `subtask_id`, `text` (live only) |
| `subtask.tool_call` | `subtask_id`, `tool`, `args` |
| `subtask.tool_result` | `subtask_id`, `tool`, `output` |
| `subtask.finished` | `subtask_id`, `result` |
| `review` | `round`, `text` (reason), `subtasks` (follow-ups) |
| `synthesis.started` | — |
| `synthesis.token` | `text` (live only) |
| `job.finished` | `status`, `synthesis`, `error` |

Token events go only to live subscribers. Everything else is stored and replayed to anyone who connects later, including after the job finishes. A subscriber that falls behind by more than 1024 events is disconnected; the chat client reconnects and skips events it has already seen, using `seq`.

## Workers

A worker runs one model with fixed settings (`--num-ctx`, `--num-predict`). For each `POST /run` it:

1. waits for one of its `--slots`;
2. builds an agent whose system message is the task's `system_prompt`, plus the current time and a note about tools;
3. runs the agent loop: call the model with the tool specs; if it asks for tools, run them and send back the results as `tool` messages; repeat until it answers or `--max-steps` is reached, at which point the last call is made without tools so the model must answer;
4. streams newline-delimited `WorkerEvent`s (`token`, `tool_call`, `tool_result`) and ends with a `result`.

Tool output is capped at 16,000 characters per call.

**Registration.** A worker posts its registration (ID, advertised URL, model, Ollama host, tags, tools, slots, current load) to `POST /workers/register` on start and every `--heartbeat` seconds. The controller considers it gone after `--heartbeat-ttl` seconds of silence, or immediately after it deregisters (`DELETE /workers/{id}`) on shutdown. If `workers.yaml` exists, only the enabled workers it lists may register, and its tags are added to theirs.

**Tools** (`internal/tools`):
- `files`: `list_files`, `read_file`, `write_file`, confined to the workspace. Paths are resolved inside the workspace root, and symlinks that lead outside are refused.
- `shell`: `run_command` runs `sh -c` (`cmd /C` on Windows) in the workspace, with a timeout. It is not a sandbox.
- `web`: `fetch_url` does an HTTP GET, reads at most 1 MB, and strips HTML tags.

## Security

- Every service listens on `127.0.0.1` unless `--host` says otherwise, and refuses a non-loopback address without a cluster token unless `--insecure` is passed.
- With a token, every request must carry `Authorization: Bearer <token>`: workers to the controller, the controller to workers, and clients to the controller.
- Traffic is plain HTTP. Across machines, use a trusted network or a VPN.
- `run_command` executes as the worker's user. Treat a worker with `shell` as able to do anything that user can.

## Storage

The controller stores `jobs` (prompt, session, status, plan, results, synthesis, error, timestamps) and `events` in SQLite (`--db`), using the pure-Go `modernc.org/sqlite` driver so the binary needs no C toolchain. Session history for the router, reviewer and synthesizer is the last `--history-turns` finished jobs in the same `session_id`.

## Concurrency and capacity

- Each worker's slots are enforced twice: by the controller's scheduler, which counts in-flight subtasks, and by the worker itself, which queues extra requests.
- When several workers share one Ollama server, Ollama decides how many requests really run at once (`OLLAMA_NUM_PARALLEL`, `OLLAMA_MAX_LOADED_MODELS`, available memory). Set worker slots to match. `GET /workers` reports each worker's `ollama_host`.
- Workers sharing one model share one loaded copy of it. Different models on one machine compete for memory; the default `workers.yaml` keeps a `granite4.2` worker disabled for that reason.

## Package layout

```text
cmd/agent-cluster/       controller, worker, chat and version subcommands (flags and env only)
internal/agent/          Ollama /api/chat client; agent loop with tools, streaming, structured output
internal/tools/          files, shell and web tools
internal/worker/         /health and streaming /run; slots; registration heartbeat
internal/controller/     HTTP API, jobs and events, planner, scheduler (pool.go), SQLite store
internal/chat/           terminal client
internal/registry/       workers.yaml allowlist
internal/schemas/        JSON types shared by every component
internal/httputil/       serving, token check, exposure check, JSON helpers
internal/fakeollama/     scriptable fake Ollama for tests
```

## References

- https://github.com/ollama/ollama/blob/main/docs/api.md (chat, tools, structured outputs)
- https://html.spec.whatwg.org/multipage/server-sent-events.html
- https://pkg.go.dev/modernc.org/sqlite
- https://github.com/peterh/liner, https://github.com/charmbracelet/lipgloss, https://github.com/charmbracelet/glamour
