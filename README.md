# agent-cluster

A general-purpose distributed agent cluster for local models. A controller breaks each request into subtasks, schedules them across worker agents on one or more machines, and merges their results into one answer. Workers run [Ollama](https://ollama.com/) models and can use tools: read and write files, run commands, and fetch web pages.

Everything runs on your own hardware. Nothing leaves your machines.

## How it works

- A **worker** runs one Ollama model. Each task arrives with its own system prompt, so workers have no fixed role. A worker can call tools in a loop until it has an answer, runs at most `--slots` tasks at once, and registers itself with the controller.
- The **controller** turns each request into a **job**:
  1. A **router** agent plans subtasks, using Ollama's structured output. A subtask can depend on others and can require a tool or tag.
  2. A **scheduler** places each subtask on a live worker with a free slot as soon as its dependencies finish, passing their results along.
  3. A **reviewer** agent checks the results and can add a follow-up round.
  4. A **synthesizer** agent streams the final answer.
- Clients follow a job's progress live as server-sent events. Jobs, events and conversation history are stored in SQLite.
- The **chat** client shows the plan, tool calls and results as they happen.

See [Architecture.md](Architecture.md) for the details.

## Quick start

You need [Ollama](https://ollama.com/download) running with a model that supports tool calling, for example:

```bash
ollama pull gemma4
```

Download `agent-cluster` for your platform from the [releases page](https://github.com/skinnyrad/agent-cluster/releases), or build it with Go 1.26+:

```bash
go build -o agent-cluster ./cmd/agent-cluster
```

Start the controller, then one or more workers, each in its own terminal:

```bash
./agent-cluster controller
```

```bash
./agent-cluster worker --id worker-1 --port 8001
```

```bash
./agent-cluster worker --id worker-2 --port 8002 --tools files,shell
```

Then talk to the cluster:

```bash
./agent-cluster chat
```

Workers register with the controller at `http://localhost:8100` by default. The repository's `workers.yaml` is an allowlist that permits `worker-1` and `worker-2`. Edit it to add workers, or delete it to allow any worker.

## Tools

Workers enable tools with `--tools` (default `files`):

| Set | Tools | What they can do |
|---|---|---|
| `files` | `list_files`, `read_file`, `write_file` | Read and write files inside the worker's workspace (`--workspace`, default `workspace/<id>`). Paths can't escape it, including through symlinks. |
| `shell` | `run_command` | Run a shell command in the workspace, killed after `--command-timeout` seconds. **This is not a security sandbox**: commands run as your user. Only enable it on machines and for prompts you trust, or run the worker in a container or VM. |
| `web` | `fetch_url` | Fetch an http(s) URL and return its text. |

Each worker has its own workspace. The router keeps subtasks that share files on the same worker, and a subtask runs on the worker that ran its dependencies when it can.

The model has to support tool calling in Ollama. Use `--tools ""` for a worker without tools.

## Running on several machines

By default every service listens only on `127.0.0.1`. To accept connections from other machines, listen on all interfaces and set a shared token. Nodes refuse to listen beyond localhost without one.

```bash
export CLUSTER_TOKEN=$(openssl rand -hex 16)   # use the same value on every machine

# controller machine
./agent-cluster controller --host 0.0.0.0

# each worker machine
./agent-cluster worker --id gpu-box --host 0.0.0.0 \
  --controller http://controller-host:8100 --advertise http://gpu-box:8001 --tags gpu

# anywhere
./agent-cluster chat --url http://controller-host:8100
```

`--advertise` is the URL the controller uses to reach the worker. Traffic is plain HTTP, so use a trusted network or a VPN.

Ollama handles one request at a time per model by default. To run several tasks on one machine at once, start Ollama with `OLLAMA_NUM_PARALLEL=N` and give the worker `--slots N`. Running different models on one machine needs enough memory to keep them all loaded.

## Using the API

| Method and path | Purpose |
|---|---|
| `POST /jobs` | Start a job: `{"prompt": "...", "session_id": "..."}`. Returns `{"job_id", "session_id"}` right away. Reuse `session_id` to continue a conversation. |
| `GET /jobs/{id}/events` | Follow a job as server-sent events: plan, subtask start/finish, tokens, tool calls, review, synthesis, and the final `job.finished`. Finished jobs replay their stored events. |
| `GET /jobs/{id}` | The job's current state. |
| `DELETE /jobs/{id}` | Cancel a running job. |
| `GET /jobs?session_id=...` | Recent jobs, optionally for one conversation. |
| `POST /dispatch` | Run a job and wait for it (synchronous). |
| `GET /workers` | Registered workers, with load, tools and liveness. |

```bash
curl -s -X POST localhost:8100/jobs -H 'Content-Type: application/json' \
  -d '{"prompt": "Write a Python bubble sort in sort.py with tests, run them, and report."}'
curl -N localhost:8100/jobs/<job_id>/events
```

Add `-H "Authorization: Bearer $CLUSTER_TOKEN"` when the cluster uses a token.

## Configuration

Every flag has an environment variable; run `agent-cluster <command> -h` for the full list.

**Controller:** `--port` (8100), `--db` (`agent-cluster.db`), `--workers` (allowlist, `workers.yaml`), `--router-model` and `--synthesizer-model` (`gemma4`), `--max-rounds` (2; 1 turns off the reviewer), `--worker-timeout` (300 s per subtask), `--history-turns` (6), `--num-ctx` (16384), `--num-predict` (4096).

**Worker:** `--id`, `--model` (`gemma4`), `--port` (8001), `--controller`, `--advertise`, `--tools`, `--workspace`, `--tags`, `--slots` (1), `--max-steps` (8 model calls per task), `--num-ctx` (16384), `--num-predict` (4096).

**Chat:** `--url` (`http://localhost:8100`), `--timeout` (900 s per job). Use `GLAMOUR_STYLE=light` on a light terminal.

**Everything:** `--token` / `CLUSTER_TOKEN`, `--host`, `OLLAMA_HOST` (default `http://localhost:11434`).

`--num-ctx` sets each model's context window explicitly. Ollama's default for some models is far larger and can use a lot of memory.

## Chat commands

| Command | Description |
|---|---|
| `/workers` | Registered workers and their load |
| `/jobs` | Recent jobs in this conversation |
| `/clear` | Start a new conversation |
| `/help` | Show commands |
| `/quit` | Exit |

Press Ctrl-C while a job runs to cancel it.

## Development

```bash
go test -race ./...
```

The tests run real controllers and workers in-process against a fake Ollama (`internal/fakeollama`), so they need no models. Pushing a `v*` tag builds release archives for Linux, macOS and Windows.
