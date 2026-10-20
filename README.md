# ctxtop

`top` for local LLMs. ctxtop finds the inference servers running on your machine and shows, for each model, how fast it's going, how full its context is and how much memory it's using. Then it tells you what to change.

![ctxtop watching llama-server and Ollama: a slot fills to 100% and ctxtop says to restart llama-server with a larger -c](docs/demo.gif)

> **Status: early.** ctxtop reads llama.cpp's `llama-server`, Ollama and LM Studio in a live terminal view and tells you what to change. Not released yet.

## Install

```sh
brew install --cask iBvishal/tap/ctxtop
```

Or download a binary for macOS (Apple Silicon or Intel) or Linux from [Releases](https://github.com/iBvishal/ctxtop/releases), or build it with Go 1.26 or later:

```sh
go install github.com/iBvishal/ctxtop@latest
```

Release archives come with build provenance: `gh attestation verify <archive> --repo iBvishal/ctxtop`.

## Use

```sh
ctxtop                              # live view; q quits, r rescans for servers
ctxtop --once                       # print one snapshot and exit
ctxtop --json                       # one snapshot as JSON, for scripts and agents
ctxtop doctor                       # check once, print only the advice
ctxtop serve                        # Prometheus metrics on 127.0.0.1:9464/metrics
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

ADVICE
▲ gemma-3-4b-it-Q8_0.gguf, slot 0  Context is 89% (7,263 of 8,192 tokens) full.
    When it fills, generation stops and the reply is cut off.
    → Restart llama-server with a larger -c. It's split across 2 slots, so
      each gets 8,192 of -c 16,384.
▲ qwen3:14b  2.2 GB of 9.6 GB doesn't fit in GPU memory and runs from CPU RAM,
    so generation is much slower.
    → Use a smaller quant or a smaller model.

RECENT REQUESTS
  12:04:31  ollama    /api/chat                200  12.48s

q quit  r rescan  every 1s
```

The live view refreshes every second (`--interval`) and looks for new servers every 5 seconds. When output isn't a terminal, ctxtop prints one snapshot instead. Speed in a snapshot is measured between two polls one second apart (`--sample`).

## Advice

Each rule names the problem, the evidence and one fix. Thresholds are starting points.

| Rule | Fires when | Level |
| --- | --- | --- |
| `gpu-spill` | Part of a model runs from CPU RAM (Ollama reports the split) | warn; critical when half or more spilled |
| `context-full` | A slot's context is 85% full or more, or the last request filled it | warn; critical at 98% |
| `queued` | Requests are waiting for a free slot (LM Studio reports its queue) | warn |
| `swap-growing` | Swap grew 100 MB or more, at 50 MB/min or faster, over at least 2 minutes, while models are loaded | warn |
| `memory-pressure` | macOS memory pressure is warn or critical | as reported |
| `unloading-soon` | An Ollama model unloads within a minute while requests are still coming | info |

When memory is short but the loaded models are small, ctxtop says other apps are the cause rather than pointing at a model.

`ctxtop doctor` exits 0 when healthy (info only), 1 on warnings and 2 on anything critical, so it works in scripts. A single check can't see swap trends; the live view can.

## Metrics

`ctxtop serve` polls in the background (every 5 seconds, or `--interval`) and serves the latest reading in Prometheus text format at `http://127.0.0.1:9464/metrics`. Use `--addr` to listen elsewhere. Add it to Prometheus as a scrape target:

```yaml
scrape_configs:
  - job_name: ctxtop
    static_configs:
      - targets: ["127.0.0.1:9464"]
```

Everything is a gauge, labelled by `runtime`, `url`, `model` and `slot` where they apply:

| Metric | What it is |
| --- | --- |
| `ctxtop_slot_generation_tokens_per_second`, `ctxtop_slot_prompt_tokens_per_second` | Speed between the last two polls |
| `ctxtop_slot_context_fill_ratio`, `ctxtop_slot_context_used_tokens`, `ctxtop_slot_context_size_tokens`, `ctxtop_slot_busy` | Context fill per slot |
| `ctxtop_model_memory_bytes`, `ctxtop_model_memory_gpu_bytes`, `ctxtop_model_memory_cpu_bytes` | Model memory, and how much spilled to CPU RAM |
| `ctxtop_model_queued_requests`, `ctxtop_model_parallel_slots`, `ctxtop_model_context_size_tokens`, `ctxtop_model_unload_timestamp_seconds` | Queue, slots, context and unload time |
| `ctxtop_server_up`, `ctxtop_server_info` | Each server found, and whether it could be read |
| `ctxtop_host_memory_*_bytes`, `ctxtop_host_swap_*_bytes`, `ctxtop_host_memory_pressure` | Memory, swap and pressure (0 normal, 1 warn, 2 critical) |
| `ctxtop_host_gpu_utilization_ratio`, `ctxtop_host_gpu_memory_used_bytes`, `ctxtop_host_power_watts` | GPU and power (macOS) |
| `ctxtop_advice{rule,level,subject}` | 1 for each piece of advice that applies now |

Metrics a runtime doesn't report are left out rather than set to 0. Alert on `ctxtop_advice{level="critical"}` to get the same checks as `ctxtop doctor`.

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

ctxtop only sends `GET` requests to servers on your own machine and runs the `lms` CLI read-only. `ctxtop serve` listens on 127.0.0.1 unless you pass another `--addr`; its metrics include model names but never prompts or output. It never shows prompt or output text: LM Studio's log stream includes the generated text, and ctxtop decodes only the stats from it. Nothing is sent anywhere else and there is no telemetry.

Polling Ollama adds `GET /api/ps` and `GET /api/version` lines to Ollama's own access log.

## License

MIT
