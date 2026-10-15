# Known Limitations and Performance Baseline (limitation.md)

> Version: v1.0 | Status: continuously recorded, updated as the implementation evolves
> Scope: known limitations of the current implementation, performance baseline, and future optimization directions

---

## 1. Pure-Filter Performance vs. Native grep

### 1.1 Background

A common R&D scenario is "filter lines containing any of several keywords" (multi-keyword OR).
serialcat uses the `filter:grep` node plus the built-in `bgrep` regex filter to persist to disk,
benchmarked head-to-head against system `grep -a -e A -e B ...` on the same input.

**Test sample**: `raw/changed_hostname_die_0924.txt` (~221MB, 2,210,841 lines).
7-way multi-keyword OR, 8960 lines of output (both tools agree on hit count).

| Tool | Time | Throughput | Hits |
|---|---|---|---|
| System `grep -a -e ...` (DFA) | 7.17s | ~30MB/s | 8960 |
| `serialcat run` (current batched build) | 12.86s | ~17MB/s | 8960 |

serialcat is currently about **1/1.8** the speed of grep.

### 1.2 Root Cause

The gap is mainly in the **regex matching algorithm**, not the transport layer:

- `grep` is built on a **DFA**, with a Boyer–Moore-style fast pre-scan for literal substrings; the keywords in this scenario
  are mostly literal substrings, exactly the input DFA is best at.
- Go's `regexp` uses **RE2** (linear but no pre-scan), scanning byte by byte, with no allocation optimizations.
- Each line also carries the overhead of `Scanner.Text()` copying + `Item` allocation + channel transport; grep does byte matching
  directly on an in-memory buffer, zero-copy, zero-allocation.

> Note: this batch refactor already improved the 221MB processing from 18s to 12.86s (~12MB/s → 17MB/s,
> i.e. ~140k lines/s → 172k lines/s). The remaining gap is an inherent difference in matching engines, not a defect of batching.

### 1.3 Positioning Statement

serialcat's positioning is a **pipeline** for "filtering + subsequent custom processing (LLM / script / chart / alert)";
the single filtering step cannot catch up with native grep in the short term. This is an engine-selection difference and an **accepted** trade-off.

### 1.4 Optional Optimization Directions (not implemented, recorded for reference)

- **Literal substring fast pre-scan**: for purely literal keywords, run a `bytes.Index` fast path first, and only enter the regex on a hit.
- **Pure-literal branch**: when the match string contains no regex metacharacters, use `strings.Contains`, avoiding RE2's byte-by-byte scan.
- The two above can approach grep's throughput without introducing external dependencies.

---

## 2. Other Known Limitations

- **Serial agent is Windows-only**: `source:serial-remote` is a cross-platform pure-HTTP node, but `serialcat agent`, which provides the physical serial capability, can only run on Windows (the `go.bug.st/serial` implementation behind `//go:build windows`). On macOS/Linux the agent only returns unsupported; the same applies to the local-direct `source:serial` and local writes.
- **The real-time serial stream does not go through `python -m http.server`**: http.server can only serve static files (used for deployment/callbacks); the real-time serial stream uses the agent's own `/api/stream` long connection; write commands go through `/api/write` (enabled by the `send` command).
- **Continuous sources have no natural EOS**: a serial port is a continuous stream with no "finished reading" concept. On disconnect, `serial-remote` auto-reconnects (with a gap marker and exponential backoff), ending only on process cancellation or permanent errors (e.g. 4xx).
- **Local blind send has no echo**: `send --port` is a send-and-exit blind send; it cannot capture the device echo; command echoes mixed into the scrolling log are indistinguishable. To see the effect, let run/agent hold the port and send via `--url` instead.
- **`filter:grep`'s `match` only supports a single regex**: configuring an array (multi-keyword OR) reports "match should be a string"; use regex `|` instead (see `docs/roadmap.md`, already listed as an extendable item).
- **`agent serve` must listen on a non-loopback address to be reachable by peers**: it binds `:7100` (0.0.0.0) by default for cross-machine direct access; specify `--listen` explicitly to restrict to a particular NIC.
- **Backfill placeholder**: long-term scope such as network sources, LLM analysis, and alert governance will be backfilled as implementation progresses.
