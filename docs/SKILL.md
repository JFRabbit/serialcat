---
name: serialcat-agent
description: >-
  Pull serial logs in real time via the serialcat agent's HTTP interface
  (/api/stream), query history (/api/logs with pagination + filtering + tail
  snapshot), and send commands to serial devices (/api/write). Use when an AI
  needs to read logs from a Windows machine with a serial device attached,
  continuously monitor serial output, or investigate serial data from a past
  time window.
---

# SKILL — Let an AI Access Serial Logs via serialcat agent

> This document is for **AI collaborators / code agents** on "how to use serialcat agent".
> It targets usage/integration, not secondary development (see the root README.md for architecture).

## 1. What It Is

`serialcat` is a lightweight local log tool. Its **agent mode** runs on a **Windows machine with a serial device attached**,
exposing the serial data over the network so the **other end** (Mac / another machine / AI) can pull it over HTTP:

- **Watch in real time** → `/api/stream` (long-lived stream)
- **Review history** → `/api/logs` (reads the agent's full persisted log, with pagination/filtering/tail snapshot)
- **Send commands** → `/api/write` (requires the agent to have the write switch enabled)

Why it matters for an AI: with a single integration into this agent, both **real-time monitoring** and **post-hoc review** of serial logs use the same HTTP interface — well-structured and easy to consume.

## 2. Prerequisite: Get the Agent's Connection Info

Before calling, you need the following provided on-site (or ask the user):

- The agent machine's **IP + port** (default `:7100`, i.e. `http://<ip>:7100`)
- Whether the **write switch `--enable-write`** is enabled, and the **write token `--token`** (only needed for write commands)

**Important boundaries**
- The agent is **currently Windows-only** (needs a real serial port); your mac/Linux can only act as a **consumer** pulling from it.
- The agent **auto-selects the serial port and auto-increments the port by +1 if occupied**; pick the reachable one from the locally accessible IPs it prints.
- The agent persists the serial stream **in full to disk** by default at `logs/serialcat-serial-*.log` (flushed line by line, with millisecond timestamps).
  The history interface `/api/logs` reads this persisted file; you **don't need to care about the filename** — the interface locates it for you.

## 3. Probing and Status

```
# liveness
curl -s http://<ip>:7100/health            # → ok

# enumerate serial ports (JSON array)
curl -s http://<ip>:7100/api/ports
# → ["COM3","COM5"]

# probe usable baud rates for a port (optional)
curl -s "http://<ip>:7100/api/probe?port=COM5"
```

## 4. Real-Time Logs (get the latest, keep following)

`/api/stream` is an **NDJSON long-lived stream**:

```
curl -N "http://<ip>:7100/api/stream"
```

- One JSON frame per line, `Content-Type: application/x-ndjson`, flushed as the agent reads.
- Each frame mainly contains a **timestamp `ts`** + **raw text `text`** (may also carry `src`/`meta` and other metadata).
- **The stream drops when the network disconnects**: a robust consumer should re-run `curl -N` to auto-reconnect after a drop, and tolerate the "data gap during the disconnect".

## 5. History Logs (review the past, paginated + filtered)

`/api/logs` reads the agent's full persisted history and returns it as **paginated JSON**. Two ways to fetch:

### 5.1 Tail snapshot (just the most recent N entries)

```
curl -s "http://<ip>:7100/api/logs?tail=true&limit=100"
```

`tail=true` returns the most recent `limit` entries at once (in chronological order); cannot be combined with `after/until`.

### 5.2 Cursor paging (pull everything from the start / from a point in time, page by page)

```
curl -s "http://<ip>:7100/api/logs?after=2026-10-08%2011:41:21.365&limit=100"
```

**Response shape**

```
{
  "count": 100,                 // number of rows actually returned in this batch
  "hasMore": true,              // false = reached the end/until, everything pulled
  "next": "2026-10-08 11:41:24.512",  // only non-empty when hasMore; feed into after next time
  "rows": [
    { "ts": "2026-10-08 11:41:21.365", "text": "<raw log line (may contain device formatting/ANSI)>" }
  ]
}
```

**Full-pull loop semantics**: the client just keeps filling `next` into the next `after` while `hasMore`, until `hasMore=false`.

```
after=""
for {
  resp = GET /api/logs?after=<after>&limit=100&...
  for row in resp.rows: output(row)
  if !resp.hasMore: break
  after = resp.next
}
```

**Parameter table**

| Parameter | Meaning | Default / limit |
|---|---|---|
| `after` | Only rows with `ts > after` (cursor paging) | None = from the head of the file |
| `until` | Only rows with `ts <= until` | None = to the tail of the file |
| `limit` | Max rows in this batch | 1000 / up to 10000 |
| `filter` | Single-layer regex; only matching rows returned (de-noise) | None = everything |
| `tail` | Set to `true` for a tail snapshot | default false |

- **Timestamp format**: `2006-01-02 15:04:05.000` (seconds.milliseconds). `after/until` use the same format as `ts`.
  Because the URL contains **spaces and colons**, prefer `url.Values` encoding or write spaces as `%20`.
- **Regex**: Go's `regexp` syntax (same as the `filter:grep` node). Example: `filter=fail|error`.
- **Errors**: persistence disabled / file missing → `404`; `after>until`, `after` combined with `tail`, `limit` over the cap, invalid time format, or regex compile failure → `400`.

## 6. Sending Commands (write data to the serial port)

```
# requires the agent to be started with --enable-write; if --token is set, include Authorization
curl -s -X POST \
  -H "Authorization: Bearer <token>" \
  "http://<ip>:7100/api/write?data=<urlencoded command>"
```

- Write switch not enabled → `403` (have the agent add `--enable-write` and restart first).
- Token set but missing/wrong → `401`.
- `data` supports literal escapes `\r` `\n` `\t` (replaced with the real control characters); the sending side handles idempotency uniformly.
- Success → `{"ok":"sent"}`.
- **Simplex note**: the serial port is a **one-way stream**; this write **does not return the device echo** (`?data=...` only sends).
  To see the device's reply, use `/api/stream` or `/api/logs` — both read the same data.

## 7. Step-by-Step Workflow for AI (recommended)

1. First `GET /health` to confirm reachability; if unreachable, ask for the IP/port.
2. For **real-time monitoring** → `curl -N /api/stream`, consume line by line, auto-reconnect on drops.
3. For **investigating a past time window** → `/api/logs` + `after/until/filter` pagination, `while(hasMore)` with `next` until done.
4. For **errors only** → add `&filter=fail|error|assert` first to shrink the volume.
5. For **sending firmware/debug commands** → confirm `--enable-write` is on, get the token, `POST /api/write`.
6. To pull serial logs into serialcat's own DAG processing → use the `source:serial-remote` node in the config (see `docs/usage.md`).

## 8. Common Pitfalls

- **Logs are passed through raw**: `text` may contain the device's ANSI escapes / internal timestamps; the interface does not process them, so don't expect clean output.
- **Persistence starts at the current startup**: after restarting the agent, `/api/logs` only reads the new file; history from previous runs is outside this interface's scope.
- **Multiple lines in the same millisecond**: the cursor `next` resumes strictly by `ts > after`; in the extreme case where "lines in the same millisecond exceed limit", a few may be missed — practically unreachable at current data volumes.
- **Port auto-incremented by +1**: the port you were told may have been offset by an occupied port; trust what the agent actually prints.
