# serialcat Roadmap (roadmap.md)

> For **outcomes/evolution**: covers "what nodes exist today, what they can do, and what's planned next".
> Detailed usage lives in `docs/usage.md`; architecture is in the root `README.md`.

## 1. Current State: Good Enough for Daily Use

serialcat already covers the two main threads of serial log processing; **daily use does not need to wait for new features**:

- **Watch serial in real time**: `agent` on Windows collects + remote streaming and printing, with auto-reconnect on disconnect.
- **Filter logs locally**: read file + grep + print / persist to disk.
- **Send commands to the serial port**: `send` remote write (with echo) or local blind send.
- **Self-update**: `update` pulls the latest version from the release source.

In short: **the current version already satisfies day-to-day firmware/serial debugging needs.**

---

## 2. Implemented Nodes

Referenced in configs via `kind:type`. The full list currently available:

| kind:type | Purpose | Example parameters |
|---|---|---|
| `source:file` | Read logs from a file line by line | `path` (supports `{ts}` timestamps) |
| `source:serial-remote` | Pull a serial stream from a remote agent (cross-platform) | `url` |
| `source:serial` | Connect to a local serial port to read (Windows only) | `port` (optional, auto-detected), `baud` |
| `filter:grep` | Regex filtering; only matching lines flow on | `match` |
| `process:script` | Hand a batch of logs to an external script for processing | `command`, `args` |
| `sink:console` | Print to screen | — |
| `sink:file` | Write to a file (auto-creates parent directories) | `path` (optional, supports `{ts}`) |

> `filter:grep`'s parameter `match` is currently a **single regex**; for multi-keyword OR scenarios, use the regex `|` form (e.g. `"fail|null|error"`).

---

## 3. Capability Boundaries

Some areas are "good enough but not polished"; the current state is stated explicitly to avoid misunderstanding:

- **Plugins (process) are only a start**: today there is only `process:script` — JSON data is passed to the external script via stdin/stdout, and the tool only acts as a "communication shell", not the host of the script's runtime. **Whether to extend (e.g. streaming round-trips, more protocols) depends on real needs; no over-engineering for now.**
- **Serial direction**:
  - Read: both agent streaming and local direct connection are supported.
  - Write: `send` can send commands, but a "send-side write node inside the DAG config" is not planned (commands are issued at operational time, not part of the pipeline config).
- **Local writes**: Windows only.

---

## 4. Future Plans (demand-driven, no speculative features)

The items below are **not to be done now**, but have a clear direction and will be advanced when needed. Priority, top to bottom:

| Item | Description | Trigger condition |
|---|---|---|
| **WebUI** | Reuse the serial streaming and send interfaces; view logs in split windows and send commands from a browser | When there is a browser-visualization need |
| **Plugin protocol extension** | Currently a single stdio protocol; extend if streaming round-trips / unstructured data are needed | When there are real complex plugin scenarios |
| **Multi-keyword filtering** | Let `filter:grep` accept an array (`match: [A, B]`), equivalent to OR | Common need; can be done any time |
| **Better reconnect/persist UX** | e.g. "no manual persist sink needed; done automatically by default" | When it feels awkward in practice |
| **Finer DAG semantic validation** | Further constrain connection rules between node kinds | When configs get complex and need stronger self-checks |

> Directional-but-deferred candidates are recorded under "Optional optimization directions" in `docs/limitation.md`; the planning is **demand-driven** across the board, no upfront work.

---

## 5. Explicitly Out of Scope (avoiding over-engineering)

- **No server-side resident framework / distributed system / RPC**: keep it a lightweight local tool.
- **No bundled script runtimes** (e.g. Python): external plugins provide their own environment on the executing machine.
- **No full frontend ecosystem beyond the WebUI**: the frontend is only an entry point to "view/operate again", not the core.
- **No preset alert/analysis/LLM chain**: these are long-term directions in the design blueprint and will only be invested in when there is strong demand.

> The full blueprint for the design and the "out of scope" list lives in `docs/design.md` (historical design document, complementary to this file).
