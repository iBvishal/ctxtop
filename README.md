# ctxtop

`top` for local LLMs. ctxtop finds the inference servers running on your machine and shows, for each model, how fast it's going, how full its context is and how much memory it's using. Then it tells you what to change.

> **Status: early.** ctxtop reads llama.cpp's `llama-server`, Ollama and LM Studio and prints one snapshot. The live terminal view and the advice pane are on the way.

## Install

```sh
go install github.com/iBvishal/ctxtop@latest
```

A Homebrew tap comes with v0.1.

## Use

```sh
ctxtop                              # find servers and print a snapshot
ctxtop --json                       # the same, as JSON for scripts and agents
ctxtop --url http://127.0.0.1:8081  # read one server, skip discovery
```

```text
ctxtop  darwin/arm64  memory 12.5 GB / 16.0 GB  swap 1.0 GB / 2.0 GB  pressure normal

llama.cpp  http://127.0.0.1:8080  pid 4120
  gemma-3-4b-it-Q8_0.gguf  ctx 8,192
      SLOT  STATE        CONTEXT  FILL  GEN t/s  PROMPT t/s
         0   busy  7,263 / 8,192   89%     51.0           -
         1   idle      0 / 8,192    0%        -           -

ollama  http://127.0.0.1:11434  pid 977
  qwen3:14b  Q4_K_M  ctx 8,192  mem 9.6 GB (7.4 GB GPU, 2.2 GB CPU)  unloads in 4m30s
      SLOT  STATE        CONTEXT  FILL  GEN t/s  PROMPT t/s
         0   busy  1,485 / 8,192   18%     11.2           -
    recent requests
    12:04:31  /api/chat  200  12.48s
```

Speed is measured between two polls one second apart (`--sample` changes the gap).

## What it reads

| Runtime | Source | What you get |
| --- | --- | --- |
| llama.cpp | `GET /props`, `GET /slots` | Per-slot context fill and live speed. `/slots` is on by default. |
| Ollama | `GET /api/ps`, its runner's `/slots`, its access log | Loaded models, memory in GPU vs CPU, unload time, recent requests. Recent Ollama runs each model in a child `llama-server`; ctxtop reads that runner for live speed and context fill. Ollama itself has no metrics endpoint. |
| LM Studio | `lms ps --json`, `lms log stream --json`, `GET /api/v0/models` | Loaded models, status, queue and parallel slots, and speed and time to first token for each finished request. Needs the `lms` CLI; without it, only the model list. |

Servers are found by matching listening sockets to processes (`lsof`), then by probing the default ports (8080, 11434, 1234). Memory, swap and macOS memory pressure come from the kernel, without sudo.

A one-off snapshot only catches what happens during its one-second sample. Recent requests from LM Studio's log stream mostly show up in the live view (coming next).

## Privacy

ctxtop only sends `GET` requests to servers on your own machine and runs the `lms` CLI read-only. It never shows prompt or output text: LM Studio's log stream includes the generated text, and ctxtop decodes only the stats from it. Nothing is sent anywhere else and there is no telemetry.

Polling Ollama adds `GET /api/ps` and `GET /api/version` lines to Ollama's own access log.

## License

MIT
