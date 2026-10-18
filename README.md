# ctxtop

`top` for local LLMs. ctxtop finds the inference servers running on your machine and shows, for each model, how fast it's going, how full its context is and how much memory it's using. Then it tells you what to change.

> **Status: early.** ctxtop reads llama.cpp's `llama-server`, Ollama and LM Studio in a live terminal view. The advice pane is on the way.

## Install

```sh
go install github.com/iBvishal/ctxtop@latest
```

Needs Go 1.26 or later. A Homebrew tap comes with v0.1.

## Use

```sh
ctxtop                              # live view; q quits, r rescans for servers
ctxtop --once                       # print one snapshot and exit
ctxtop --json                       # one snapshot as JSON, for scripts and agents
ctxtop --url http://127.0.0.1:8081  # read one server, skip discovery
```

```text
ctxtop  Apple M4
MEM ██████████░░ 12.5 GB / 16.0 GB   SWAP 1.0 GB   PRESSURE normal
GPU ██████████░░  81%   GPU MEM 0.9 GB   POWER cpu 4.2 W  gpu 9.8 W

llama.cpp  127.0.0.1:8080  pid 4120  b11669
  gemma-3-4b-it-Q8_0.gguf  ctx 8,192
    slot 0  busy  ███████████░  89%  7,263 / 8,192   gen 51.0 t/s
    slot 1  idle  ░░░░░░░░░░░░   0%  0 / 8,192

ollama  127.0.0.1:11434  pid 977  0.40.6
  qwen3:14b  Q4_K_M  ctx 8,192  mem 9.6 GB (7.4 GB GPU, 2.2 GB CPU)  unloads in 4m30s
    slot 0  busy  ██░░░░░░░░░░  18%  1,485 / 8,192   gen 11.2 t/s

RECENT REQUESTS
  12:04:31  ollama    /api/chat                200  12.48s

q quit  r rescan  every 1s
```

The live view refreshes every second (`--interval`) and looks for new servers every 5 seconds. When output isn't a terminal, ctxtop prints one snapshot instead. Speed in a snapshot is measured between two polls one second apart (`--sample`).

## What it reads

| Runtime | Source | What you get |
| --- | --- | --- |
| llama.cpp | `GET /props`, `GET /slots` | Per-slot context fill and live speed. `/slots` is on by default. |
| Ollama | `GET /api/ps`, its runner's `/slots`, its access log | Loaded models, memory in GPU vs CPU, unload time, recent requests. Recent Ollama runs each model in a child `llama-server`; ctxtop reads that runner for live speed and context fill. Ollama itself has no metrics endpoint. |
| LM Studio | `lms ps --json`, `lms log stream --json`, `GET /api/v0/models` | Loaded models, status, queue and parallel slots, and speed and time to first token for each finished request. Needs the `lms` CLI; without it, only the model list. |

Servers are found by matching listening sockets to processes (`lsof`), then by probing the default ports (8080, 11434, 1234).

On macOS, everything about the machine is read without sudo: memory, swap and memory pressure from the kernel; GPU use and GPU memory from the Apple GPU driver's statistics; CPU and GPU power from IOReport's energy counters, sampled every 5 seconds. ctxtop never runs `powermetrics`.

A one-off snapshot only catches what happens during its one-second sample. Recent requests from LM Studio's log stream mostly show up in the live view.

## Privacy

ctxtop only sends `GET` requests to servers on your own machine and runs the `lms` CLI read-only. It never shows prompt or output text: LM Studio's log stream includes the generated text, and ctxtop decodes only the stats from it. Nothing is sent anywhere else and there is no telemetry.

Polling Ollama adds `GET /api/ps` and `GET /api/version` lines to Ollama's own access log.

## License

MIT
