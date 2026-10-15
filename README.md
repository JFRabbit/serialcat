# logdiag

> 轻量本地流式日志处理工具，单二进制 CLI。面向固件串口等常见日志处理场景。
> 这套 `README.md` 面向**开发人员**讲项目架构与功能；**使用方式**见 [docs/usage.md](docs/usage.md)。

## 功能概览

- **实时看串口**：Windows 上 `agent` 采集串口流，跨平台 `serial-remote` 拉流打印；断线自动重连并标记断档。
- **本地筛日志**：读文件 + grep 过滤 + 打印/落盘。
- **向串口下发命令**：`send` 命令远程写（`/api/write`，有回显）或本地盲发。
- **自更新**：`update` 从发布源（MinIO）拉取替换二进制。

## 快速开始

```bash
go build -o logdiag .
./logdiag run -f examples/dev_grep.yaml   # 读文件筛日志
./logdiag version
```

## 目录结构

```
main.go            → cmd.Execute()
cmd/               Cobra 薄层：run / verify / version / update / agent / send
internal/          agent（串口代理）、app（run 装配）、logger、updater、constant
pkg/               可复用库，勿删
  ├─ dag/          DAG 引擎：goroutine + channel 流式，批量 []Item，扇出/背压/EOS
  ├─ config/       Config 模型（唯一权威）+ 拓扑校验（环检测/种类方向）
  ├─ nodes/        source/filter/process/sink 节点实现
  └─ serial/       串口驱动抽象（Windows 真实现，其它平台报 unsupported）
plugins/           用户插件（外部脚本）
scripts/           临时部署脚本（非发布产物）
docs/              usage（使用）、roadmap（演进）、limitation（已知限制）、design（历史蓝图）
```

## 架构要点

- **Config（Go struct）为唯一权威**：YAML 与（未来的）WebUI 都只是映射到同一配置模型的入口。
- **DAG 引擎**（`pkg/dag`）：传输单元为**批量 `[]Item`**。每边默认缓冲 128 批做背压；扇入（writer 计数）合并、扇出（广播复制）、EOS 归零传播。外部保持流式语义。
- **节点协议**（`pkg/nodes`）：
  - `source`：无入边，`Out() <-chan []Item` 发射。
  - `filter`/`process`/`sink`：读入处理，可选输出（sink 无输出）。
  - `process:script` 为**子进程通信壳**：stdlib NDJSON over stdin/stdout，工具只做壳、不背脚本运行环境。
- **配置校验**（`pkg/config`）：结构级（id 唯一、type 齐全、source 无 from、非 source 必有 from）+ 语义级（kind 合法、sink 不能作上游）+ **环检测**（Kahn 拓扑），`verify` 与每次 `run` 前都会执行。
- **串口驱动下沉**到 `pkg/serial`（规避 pkg import internal 的 Go 规则）：仅 Windows 有物理实现，其它平台返回 unsupported。
- **日志隔离**：工具自身日志写运行目录 `logs/logdiag.log`；console 节点输出独立走 stdout。
- **自更新**：版本经 `-ldflags -X logdiag/cmd.Version=...` 注入，更新源前缀 `-X logdiag/cmd.DefaultUpdateURL=...`；`update` 从 `{baseURL}/logdiag/version.json` 拉取最新清单并按平台替换二进制。

## 关键命令

| 命令 | 说明 |
|---|---|
| `logdiag run -f x.yaml` | 执行 DAG 配置流水 |
| `logdiag verify -f x.yaml` | 校验配置拓扑 |
| `logdiag version [--short]` | 打印版本 |
| `logdiag update [--check]` | 检查/自更新 |
| `logdiag agent serve ...` | Windows 串口代理 |
| `logdiag agent probe [port]` | 枚举串口 / 探测波特率 |
| `logdiag send ...` | 向串口下发命令 |

## CI / 发版

- `.gitlab-ci.yml`：Go 测试 → 多平台编译 → mc 上传产物（二进制 + `usage.md` + `version.json`）到 MinIO。
- 发版命令示例（本地）：

```bash
go build -ldflags "-X logdiag/cmd.Version=1.0.0 -X logdiag/cmd.DefaultUpdateURL=http://ip/bucket" -o logdiag .
```

## License

logdiag 以 **Apache License 2.0** 开源，见 [LICENSE](LICENSE)。版权归 **JFRabbit**（见 [NOTICE](NOTICE)）。

使用的第三方依赖及各自许可证见 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)。

> 详细使用见 [docs/usage.md](docs/usage.md)；演进规划见 [docs/roadmap.md](docs/roadmap.md)；已知限制与性能基线见 [docs/limitation.md](docs/limitation.md)。