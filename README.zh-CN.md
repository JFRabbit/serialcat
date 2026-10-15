# serialcat

[![CI](https://github.com/JFRabbit/serialcat/actions/workflows/ci.yml/badge.svg)](https://github.com/JFRabbit/serialcat/actions)
[![License](https://img.shields.io/github/license/JFRabbit/serialcat)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/JFRabbit/serialcat)](go.mod)

> 轻量级、单二进制的流式日志处理工具，面向固件串口等日常日志处理场景：实时看串口、本地筛日志、向下发命令、自更新。

**语言：** [English](README.md) · [中文](README.zh-CN.md)

**文档：** [使用指南](docs/usage.md) · [架构笔记](docs/design.md) · [演进规划](docs/roadmap.md) · [已知限制](docs/limitation.md) · [参与贡献](CONTRIBUTING.md) · [安全](SECURITY.md) · [行为准则](CODE_OF_CONDUCT.md)

## 功能概览

- **实时看串口**：在持有串口的 Windows 机器上跑 `agent` 采集串口流，任意平台用 `serial-remote` 节点拉流打印；断线自动重连并标记断档。
- **本地筛日志**：读文件 + 正则 grep 过滤，打印到屏幕或写入文件。
- **向串口下发命令**：`send` 命令通过远程写通道（`/api/write`，有设备回显）下发，或对本地串口盲发。
- **自更新**：`update` 从 GitHub Releases 拉取最新版本并就地替换当前二进制。

## 快速开始

构建单二进制并查看版本：

```bash
go build -o serialcat .
./serialcat version
```

serialcat 的流水线由一个简短的 YAML 配置描述。最简单的例子 `file → console`（读文件并打印）：

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

先校验配置，再运行：

```bash
./serialcat verify -f dag.yaml   # 只检查配置拓扑，不执行
./serialcat run -f dag.yaml      # 执行流水线
```

更多真实场景（看远端串口、筛大日志、下发命令）见 [docs/usage.md](docs/usage.md)。

## 目录结构

```
main.go              → cmd.Execute()
cmd/                 Cobra 薄层：run / verify / version / update / agent / send
internal/            agent（串口代理）、app（run 装配）、logger、updater、constant
pkg/                 可复用引擎库，勿删
  ├─ dag/            DAG 引擎：goroutine + channel 流式，批量 []Item，扇出/背压/EOS
  ├─ config/         Config 模型（唯一权威）+ 拓扑校验（环检测）
  ├─ nodes/          source / filter / process / sink 节点实现
  └─ serial/         串口驱动抽象（Windows 真实现，其它平台报 unsupported）
plugins/             用户插件（外部脚本）——不推送
scripts/             临时运维/部署脚本——不推送
docs/                usage / roadmap / limitation / design
.github/workflows/   GitHub Actions CI（test → 矩阵构建 → tag 触发 Release）
```

## 架构要点

- **Config（Go struct）为唯一权威**：YAML 与插件都只是映射到同一配置模型的入口，不另起配置源。
- **DAG 引擎**（`pkg/dag`）：基于 goroutine + channel 的流式实现。传输单元为**批量 `[]Item`**；每条边默认缓冲 128 批做背压。扇入（writer 计数）合并、扇出（广播复制）、EOS 在 writer 归零时传播。引擎边界外保持流式语义。
- **节点协议**（`pkg/nodes`）：
  - `source`：无入边，经 `Out() <-chan []Item` 发射。
  - `filter` / `process` / `sink`：读入并处理，输出可选（sink 无输出）。
  - `process:script` 是**子进程通信壳**：stdlib NDJSON over stdin/stdout；工具只做壳，不背脚本运行环境。
- **配置校验**（`pkg/config`）：结构级（id 唯一、type 齐全、source 无 from、非 source 必有 from）+ 语义级（kind 合法、sink 不能作上游）+ **环检测**（Kahn 拓扑）。`verify` 与每次 `run` 前都会执行。
- **串口驱动下沉**到 `pkg/serial`（规避 pkg import internal 的 Go 规则）：仅 Windows 有物理实现，其它平台返回 `unsupported`；因此 `agent` 与本地写仅限 Windows。
- **日志隔离**：工具自身日志写运行目录 `logs/serialcat.log`；console 节点输出独立走 stdout，二者不混。
- **自更新 = GitHub Releases**：版本经 `-ldflags -X github.com/JFRabbit/serialcat/cmd.Version=x.y.z` 注入，更新源默认仓库 `-X github.com/JFRabbit/serialcat/cmd.DefaultUpdateURL=JFRabbit/serialcat`；`update` 解析最新 Release 并按平台替换二进制（SHA-256 校验）。

## 关键命令

| 命令 | 说明 |
|---|---|
| `serialcat run -f x.yaml` | 从配置文件执行 DAG 流水 |
| `serialcat verify -f x.yaml` | 只校验配置拓扑，不运行 |
| `serialcat version [--short]` | 打印版本（`--short` 只出版本号） |
| `serialcat update [--check]` | 检查 / 安装最新版本 |
| `serialcat agent serve ...` | Windows 串口代理（HTTP `:7100`，被占自动 +1；自动选口） |
| `serialcat agent probe [port]` | 列出本机串口 / 探测波特率 |
| `serialcat send ...` | 向串口下发命令（远程用 `--url`/`--token`，本地盲发用 `--port`） |

`serialcat -h` 与 `serialcat <命令> -h` 可随时查看完整帮助。

## CI / 发版

持续集成跑在 **GitHub Actions**（`.github/workflows/ci.yml`）：

1. 每次 push / PR 执行 `go test ./...`。
2. 构建矩阵编译 `windows/amd64`、`linux/amd64`、`darwin/amd64`、`darwin/arm64`。
3. 推送 `v*` 标签触发 **GitHub Release**，二进制以资产名 `serialcat-{os}-{arch}` 上传（Windows 加 `.exe`），并附带 `docs/usage.md`。

本地发版构建示例（经 `ldflags` 注入版本与更新源）：

```bash
go build \
  -ldflags "-X github.com/JFRabbit/serialcat/cmd.Version=1.0.0 \
            -X github.com/JFRabbit/serialcat/cmd.DefaultUpdateURL=JFRabbit/serialcat" \
  -o serialcat .
```

## License

serialcat 以 **Apache License 2.0** 开源，见 [LICENSE](LICENSE)。版权归 **JFRabbit**（见 [NOTICE](NOTICE)）。

使用的第三方依赖及各自许可证见 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)。
