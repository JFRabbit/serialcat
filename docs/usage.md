# serialcat Usage Guide

> For **end users**. This covers "how to use" without diving into technical details; for architecture and internal design see the root `README.md`.

serialcat is a lightweight local log processing tool. The most common use cases:

- Watch **serial port logs** in real time (especially the `serialcat agent` serial stream from embedded devices, viewable remotely).
- **Filter lines of interest** out of large logs, then print or save them to a file.
- **Send commands** to a serial device, treating the device's serial port as a "remotely operable terminal".
- Provide a **log access channel for code agents / AI**: serial logs can be pulled across the network, in real time and replayable, so tools and AI can consume logs reliably.

---

## 1. Installation

Download the binary matching your platform from the release page and put it in place. The command name is **`serialcat`**.

Different platforms/architectures map to different files; e.g. Windows uses `serialcat.exe`, macOS (Intel) uses `serialcat` (darwin/amd64).

> Note: serialcat itself runs on Windows/macOS/Linux for viewing remote serial logs; however, the ability to **connect a local serial port directly** (`agent` and local writes) is **currently Windows-only**.

Verify the installation:

```bash
serialcat version
```

---

## 2. Core Concept: Config File (YAML)

serialcat works around "how a log pipeline flows". This pipeline is called a **DAG** and is written in a YAML file, specified with `-f`:

```bash
serialcat run -f your-config.yaml
```

A config describes several kinds of "nodes" and the connections between them:

| Term | Role | Example |
|---|---|---|
| source | **Where** the logs **come from** | read a file / pull a serial stream / connect a local serial port |
| filter | **Filter out** lines you don't care about by rules | grep regex filtering |
| process | Hand off to an **external script** for processing | plugin |
| sink | **Where** the logs **go** | print to screen / write to file |

The simplest example `file → console` (read a file and print it):

```yaml
graph:
  - id: src
    kind: source
    type: file
    path: log.txt

  - id: out
    kind: sink
    type: console
    from: [src]
```

Key points:

- `id` is the node name; you choose it, and it must be unique within the graph.
- `from: [...]` means "this node consumes whose output". A sink must have `from`.
- A node can be referenced by multiple downstream nodes (e.g. the same log both printed and persisted to disk).

---

## 3. Common Scenarios

### 3.1 Filter keywords from a large log

```yaml
graph:
  - id: src
    kind: source
    type: file
    path: big_log.txt

  - id: err
    kind: filter
    type: grep
    match: "fail|null|error"      # regex; only matching lines flow on
    from: [src]

  - id: out
    kind: sink
    type: console
    from: [err]
```

Verify the config before running:

```bash
serialcat verify -f big_log.yaml    # only checks whether the config is valid
serialcat run -f big_log.yaml       # run it
```

> `verify` only checks without executing; it's recommended to run `verify` after every config change.

### 3.2 Watch a remote serial log (most common)

The serial port is physically attached to a **Windows** machine. First run the agent there (baud rate defaults to 115200, HTTP defaults to `:7100` — no need to pass them):

```bash
serialcat agent serve
```

After startup it prints the addresses reachable from this machine (e.g. `http://192.168.1.x:7100`); note it down. If 7100 is occupied, the port is automatically incremented by +1.

Back on the machine where you want to watch the logs, pull the stream with a `serial-remote` node:

```yaml
graph:
  - id: ser
    kind: source
    type: serial-remote
    url: "http://192.168.1.x:7100"   # the address printed by the agent

  - id: out
    kind: sink
    type: console
    from: [ser]
```

Then:

```bash
serialcat run -f serial.yaml
```

The serial log streams to the screen in real time. On disconnect it auto-reconnects and shows a "gap" notice.

> Want to keep a copy of the log on the agent machine itself? The agent persists the full stream to `logs/` in its working directory by default — no config needed.

### 3.3 Send commands to a serial device

There are **two** ways, depending on who holds the serial port.

**Method A (recommended; you can see the device echo)**: the port is held by `run`/`agent`, serialcat sends commands over the HTTP channel, and the device's echo comes back along the log stream.

- The holder enables the write channel:
  - agent: `agent serve --enable-write` (prints a token)
  - local: `serialcat run --enable-write -f config.yaml`
- Send commands (defaults to the local write channel `127.0.0.1:7100`; add `--url` and `--token` to write remotely):

```bash
serialcat send "ls -al"
serialcat send "AT+OPEN"
serialcat send --url http://192.168.1.x:7100 --token <token> "reboot"
```

**Method B (blind send)**: when the port is free and held exclusively, send directly to a local serial port; it exits right after sending and you cannot see the echo:

```bash
serialcat send --port COM5 "ls"
serialcat send --port COM5 --cmd "echo hi" arg1     # --cmd and positional args are concatenated
```

> Details: by default the command gets a trailing carriage return `\r` appended to trigger execution; if it already ends with `\r`/`\n`, no extra one is added. Use `--raw` to send exact bytes.

---

## 4. Other Commands at a Glance

| Command | Purpose |
|---|---|
| `serialcat run -f x.yaml` | Execute a DAG config pipeline |
| `serialcat verify -f x.yaml` | Only validate the config, do not run |
| `serialcat version [--short]` | Print the version number |
| `serialcat update [--check]` | Check for and update to the latest version |
| `serialcat agent serve ...` | Run the serial agent on Windows |
| `serialcat agent probe [port]` | List local serial ports / probe baud rates |
| `serialcat send ...` | Send a command to a serial device |

`serialcat -h` or `serialcat <command> -h` shows help at any time.

---

## 5. FAQ

- **`serial port busy`**: the port is occupied by another program (another agent, SecureCRT, etc.). Close the occupier, or switch to the "holder sends" method (`--url`).
- **Sent a command but nothing happened**: devices usually wait for a carriage return before executing. If your command doesn't end with `\r`, try adding it explicitly, e.g. `send "ls -al\r"`.
- **The port has continuous logs and `send --port` is awkward**: the command echo would be mixed into the scrolling log. Prefer the holder + `--url` method instead; the echo shows up in the run/agent log.

---

> For more internals, the roadmap, and known limitations, see `docs/roadmap.md` and `docs/limitation.md`.
