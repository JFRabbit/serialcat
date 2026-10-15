# design v2 — Lightweight Local Streaming Log Processing Tool

> Positioning: a **lightweight, locally-run log processing tool**. Aimed at common log processing (firmware serial is one of the primary scenarios).
> The frame of reference is Unix pipeline tools like ripgrep / jq, not distributed platforms like DolphinScheduler / Flink.
>
> Value propositions:
> 1. **Solve human log-watching** — real-time filtering and cross-network forwarding of serial logs is the most frequent, most practical daily capability.
> 2. **Give code agents / AI a log channel** — turn serial logs into a structured, cross-network, real-time and replayable (persist to disk + backfill history) access source; AI consumption compounds exponentially, offering the highest return multiplier.
>
> Corollary: **the value of the data channel > the value of the UI**. Hence the UI stays minimal (no heavy frontend ecosystem), prioritizing a well-formed protocol and data shape that programs and AI can consume reliably.

---

## 1. Core Model: DAG

The tool is a **data pipeline (directed acyclic graph)** with only two kinds of things:

- **source**: nodes with no incoming edges; data origins (file / serial / stdin / network).
- **node**: everything else; processing/output units.

Data flows over edges, unified as an **Item** stream. Nodes connect via `from` (`from` is the edge).

```
file ─▶ grep ─▶ console
        │
        └──▶ script ─▶ alert
```

### Stream semantics

- An Item is one record (a raw line), not further divisible.
- **EOS (end)**: streams with an endpoint (file EOF) broadcast completion, and each node `Flush`es its summary; streams without an endpoint (serial) wind down via windows/timeouts.
- **Fan-out**: one output feeding multiple downstream nodes; the engine copies Items.
- **Backpressure**: each edge has a bounded queue + overflow policy (drop/block), so a slow branch cannot stall the whole graph.

---

## 2. Node Classification (by function)

| Category | Examples | Description |
|---|---|---|
| Input source | file / serial / stdin / network | Produce an Item stream |
| Filter filter | grep / level / time-range | **Reduce the log**, without changing data essence; most common |
| Process process | extract / script / state / agg | Raw text → structured/derived data |
| Analysis analysis | LLM | Semantic attribution; slow and expensive, requires upstream filtering |
| Rule rule | alert | Trigger notifications by rules (window/threshold) |
| Output sink | console / file / webhook / store | The destination that consumes Items |

> Example chain: `serial → grep (reduce) → extract (extract fields) → alert (rule) → notify`

---

## 3. Config: The Config Model Is the Single Source of Truth

- Define an independent **`Config` (Go struct)** as the single source of truth.
- **YAML (the current entry point)** and a future **WebUI (forms)** are just two entry points mapping onto the same `Config`.
- Therefore "YAML → WebUI" is not a rewrite; it only adds an entry point, and the engine and config model stay unchanged.

Example config shape:

```yaml
graph:
  - id: serial        # 1. source (real-time serial)
    kind: source
    type: serial
    port: /dev/ttyUSB0
    baud: 115200

  - id: g             # 2. filter (reduce the log)
    kind: filter
    type: grep
    match: "MEM|alloc|free"
    from: serial

  - id: s             # 3. process (external script processing)
    kind: process
    type: script
    cmd: python3 ./mem_check.py
    runtime: python3>=3.8   # advisory only; not enforced/auto-installed
    from: g

  - id: out           # 4. sink (output)
    kind: sink
    type: console
    from: s
```

Edges = the `from` field; the same id can be referenced by multiple downstream nodes (fan-out).

---

## 4. Architecture Layers (the WebUI is just a future new driver)

| Layer | Responsibility | Stable across versions |
|---|---|---|
| Config | The single authoritative config model | ✅ |
| Orchestration format | YAML (this version) / future WebUI | One of the execution entry points |
| DAG engine | Independent library `pkg/dag` | ✅ Reused by any driver |
| Plugins | grep/script/alert, etc. | ✅ |
| Frontend | CLI printing (this version) → Vue 3 (future) | New layer |

- **CLI and server are just two drivers of the same dag library**: `run` (one-shot batch processing) / `serve` (future resident + WebUI connection). MVP only implements `run`.
- Keep it lightweight: in-process goroutine + channel; **no service frameworks / RPC / distributed systems**.

---

## 5. Built-in vs. External (division of responsibilities)

**Criterion: does the tool need to provide/maintain a runtime?**

- **What can be built in → built in** (single binary, ready to use, zero extra environment): file/stdin/serial, grep/extract/agg/alert, console/file. Compiled into the Go binary.
- **Arbitrary-language free logic → external `script`**: the tool only acts as a **subprocess communication shell** (spawn the process, pass JSON Lines Items via stdin, read stdout results). Runtimes (Python, etc.) are **installed by the executing machine itself**; the tool only gives friendly availability hints and **neither bundles nor auto-installs** them.

> Principle: the tool is a shell, not a runtime host. Common functionality is built in (lightweight, instant use); niche logic relies on an external escape hatch (users bring their own environment).

---

## 6. Tech Stack

- **Language**: Go. Single binary, cross-platform, `embed` can bundle the frontend.
- **LLM**: HTTP calls to an OpenAI-compatible interface, no SDK binding. LLM is only **one node plugin**, not core, implemented later.
- **WebUI**: Vue 3 (future version). Backend `net/http` + `embed`; real-time push (WebSocket/SSE) to be decided later.
- **MVP minimal nodes**: `source: file/stdin` + `filter: grep` + `sink: console/file`.

---

## 7. MVP Acceptance (first example)

Run `file → grep → console/file` and verify three things:

1. **EOS**: when the file is fully read, each node Flushes.
2. **Fan-out**: the same source can feed multiple downstream nodes.
3. **Backpressure**: a slow downstream does not block other branches.

Serial, script, alert, store, and LLM will be added progressively as nodes.

---

## Appendix: Anti-Goals (things not done)

- No distributed / resident service framework / RPC (keep it lightweight and local).
- No script runtime environment installation (Python, etc. provided by the user's system).
- No multi-tenant shared scheduling platform (not DolphinScheduler).
- LLM does not participate in alert decisions; it only does attribution analysis.
