# ctxtop

`top` for local LLMs. ctxtop finds the inference servers running on your machine and shows, for each model, how fast it's going, how full its context is and how much memory it's using. Then it tells you what to change.

> **Status: early.** Today ctxtop reads llama.cpp's `llama-server` and prints one snapshot. Ollama and LM Studio support, the live terminal view and the advice pane are on the way.

## Install

```sh
go install github.com/iBvishal/ctxtop@latest
```

A Homebrew tap comes with v0.1.

## Use

```sh
ctxtop                              # find servers and print a snapshot
ctxtop --json                       # the same, as JSON for scripts and agents
ctxtop --url http://127.0.0.1:8081  # read one llama-server, skip discovery
```

```text
ctxtop  darwin/arm64  memory 11.8 GB / 16.0 GB  swap 1.0 GB / 2.0 GB  pressure normal

llama.cpp  http://127.0.0.1:8080  pid 4120
  qwen2.5-0.5b-instruct-q4_k_m.gguf  ctx 4,096
    slot 0  busy  ctx 1,212 / 4,096 (30%)  gen 182.4 t/s
    slot 1  idle  ctx 0 / 4,096 (0%)
```

Speed is measured between two polls one second apart (`--sample` changes the gap).

## What it reads

| Runtime | Source | Notes |
| --- | --- | --- |
| llama.cpp | `GET /props`, `GET /slots` | `/slots` is on by default. Context fill counts the prompt and every generated token. |
| Ollama | `GET /api/version` | Found, not read yet. |

Servers are found by matching listening sockets to process names (`lsof`), then by probing the default ports (8080, 11434). Memory, swap and macOS memory pressure come from the kernel, without sudo.

## Privacy

ctxtop only sends `GET` requests to servers on your own machine. It never reads prompt text, sends nothing anywhere else and collects no telemetry.

## License

MIT
