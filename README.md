# serialcat

[![CI](https://github.com/JFRabbit/serialcat/actions/workflows/ci.yml/badge.svg)](https://github.com/JFRabbit/serialcat/actions)
[![License](https://img.shields.io/github/license/JFRabbit/serialcat)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/JFRabbit/serialcat)](go.mod)

> A lightweight, single-binary streaming log processing tool, built for day-to-day
> serial (firmware) log workflows: watch a serial port live, filter log files locally,
> push commands down the line, and self-update.

**Language:** [English](README.md) · [中文](README.zh-CN.md)

**Docs:** [Usage](docs/usage.md) · [Architecture notes](docs/design.md) · [Roadmap](docs/roadmap.md) · [Known limitations](docs/limitation.md) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Code of Conduct](CODE_OF_CONDUCT.md)

## Features

- **Watch a serial port live**: run `agent` on the Windows machine that owns the port to capture the serial stream; pull and print it from anywhere with the cross-platform `serial-remote` source. Auto-reconnects on drop and marks gaps.
- **Filter logs locally**: read a file, grep with a regular expression, and print to the console or write to a file.
- **Send commands down the wire**: the `send` command writes to a remote agent (`/api/write`, with device echo) or blind-sends to a local port.
- **Self-update**: `update` fetches the latest release from GitHub Releases and replaces the current binary in place.

## Quick start

Build the single binary and check the version:

```bash
go build -o serialcat .
./serialcat version
```

serialcat pipelines are described by a small YAML config. The simplest one, `file → console` (read a file and print it):

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

Validate it, then run it:

```bash
./serialcat verify -f dag.yaml   # check the topology only, does not run
./serialcat run -f dag.yaml      # execute the pipeline
```

For real-world examples (watching a remote serial port, filtering a big log, sending commands), see [docs/usage.md](docs/usage.md).

## Directory structure

```
main.go              → cmd.Execute()
cmd/                 Cobra thin layer: run / verify / version / update / agent / send
internal/            agent (serial proxy), app (run assembly), logger, updater, constant
pkg/                 Reusable engine libraries — do not delete
  ├─ dag/            DAG engine: goroutine + channel streaming, batched []Item, fan-out/backpressure/EOS
  ├─ config/         Config model (single source of truth) + topology validation (cycle detection)
  ├─ nodes/          source / filter / process / sink node implementations
  └─ serial/         Serial driver abstraction (real implementation on Windows, unsupported elsewhere)
plugins/             User plugins (external scripts) — not published
scripts/             Temporary ops/deploy scripts — not published
docs/                usage / roadmap / limitation / design
.github/workflows/   GitHub Actions CI (test → matrix build → release on tag)
```

## Architecture highlights

- **Config (a Go struct) is the single source of truth.** YAML files and plugins are only entry points that map onto the same config model; there is no secondary config source.
- **DAG engine** (`pkg/dag`): streaming built on goroutines and channels. The transport unit is a **batch of `[]Item`**; each edge buffers 128 batches by default for backpressure. Fan-in merges via writer counting, fan-out broadcasts by copy, and end-of-stream (EOS) propagates until zero writers remain. Streaming semantics are preserved at the engine boundary.
- **Node protocol** (`pkg/nodes`):
  - `source`: no incoming edges; emits via `Out() <-chan []Item`.
  - `filter` / `process` / `sink`: consume and process input; output is optional (sinks have none).
  - `process:script` is a **subprocess communication shell**: NDJSON over stdin/stdout. The tool is only a shell — it does not host the script's runtime.
- **Config validation** (`pkg/config`): structural checks (unique ids, complete types, no `from` on sources, `from` required on non-sources) plus semantic checks (valid kinds, sinks cannot be upstream) plus **cycle detection** (Kahn topological sort). Both `verify` and every `run` execute it.
- **Serial driver pushed down** to `pkg/serial` (keeps `pkg` from importing `internal`, per Go rules): only Windows has a physical implementation; other platforms report `unsupported`. `agent` and local writes therefore require Windows.
- **Log isolation**: tool logs go to `logs/serialcat.log` under the run directory; console-node output goes to stdout independently — the two never mix.
- **Self-update via GitHub Releases**: the version is injected with `-ldflags -X github.com/JFRabbit/serialcat/cmd.Version=x.y.z`, and the update source defaults to the repo via `-X github.com/JFRabbit/serialcat/cmd.DefaultUpdateURL=JFRabbit/serialcat`; `update` resolves the latest release and replaces the binary per platform (SHA-256 verified).

## Key commands

| Command | Description |
|---|---|
| `serialcat run -f x.yaml` | Execute a DAG pipeline from a config file |
| `serialcat verify -f x.yaml` | Validate config topology without running |
| `serialcat version [--short]` | Print version info (`--short` prints just the version number) |
| `serialcat update [--check]` | Check for / install the latest version |
| `serialcat agent serve ...` | Windows serial agent (HTTP `:7100`, auto-incrementing if busy; auto-selects the port) |
| `serialcat agent probe [port]` | List local serial ports / probe baud rates |
| `serialcat send ...` | Send a command to a serial port (remote via `--url`/`--token`, or local blind send via `--port`) |

`serialcat -h` and `serialcat <command> -h` show full help for every command.

## CI / Release

Continuous integration runs on **GitHub Actions** (`.github/workflows/ci.yml`):

1. `go test ./...` on every push / PR.
2. A build matrix compiles `windows/amd64`, `linux/amd64`, `darwin/amd64` and `darwin/arm64`.
3. Pushing a tag `v*` triggers a **GitHub Release** with the binaries uploaded as assets named `serialcat-{os}-{arch}` (`.exe` suffix on Windows), plus `docs/usage.md`.

To build a release binary locally (injecting the version and update source via `ldflags`):

```bash
go build \
  -ldflags "-X github.com/JFRabbit/serialcat/cmd.Version=1.0.0 \
            -X github.com/JFRabbit/serialcat/cmd.DefaultUpdateURL=JFRabbit/serialcat" \
  -o serialcat .
```

## License

serialcat is open-sourced under the **Apache License 2.0**; see [LICENSE](LICENSE). Copyright © **JFRabbit** (see [NOTICE](NOTICE)).

Third-party dependencies and their licenses are listed in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).
