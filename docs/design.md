# design v2 — 轻量本地流式日志处理工具

> 定位：**轻量、本地运行的日志处理工具**。面向常见日志处理（固件串口是主要场景之一）。
> 参照系是 ripgrep / jq 这类 Unix 管道工具，不是 DolphinScheduler / Flink 等分布式平台。

---

## 1. 核心模型：DAG

工具是一根**数据管道（有向无环图）**，只有两类东西：

- **source**：无入边的节点，数据来源（文件 / 串口 / stdin / 网络）。
- **node**：其余一切节点，是处理/输出单元。

数据在边上传送，统一为 **Item** 流。节点通过 `from` 连接（`from` 即边）。

```
file ─▶ grep ─▶ console
        │
        └──▶ script ─▶ alert
```

### 流语义

- Item 是一条记录（原始行），不可再细。
- **EOS（结束）**：有终点的流（文件 EOF）广播结束，各节点 `Flush` 吐汇总；无终点的流（串口）靠窗口/超时自行收尾。
- **扇出**：一个输出接多个下游，引擎复制 Item。
- **背压**：每条边有界队列 + 溢出策略（drop/block），避免一个慢分支拖垮全图。

---

## 2. 节点分类（按业务）

| 分类 | 例子 | 说明 |
|---|---|---|
| 输入 source | file / serial / stdin / network | 产生 Item 流 |
| 过滤 filter | grep / level / time-range | **缩小日志**，不改变数据本质，最常用 |
| 加工 process | extract / script / state / agg | 原文 → 结构化/派生数据 |
| 分析 analysis | LLM | 语义归因，慢且贵，需前级已过滤 |
| 判定 rule | alert | 按规则（窗口/阈值）触发通知 |
| 输出 sink | console / file / webhook / store | 消费 Item 的落点 |

> 示例链路：`serial → grep(缩小) → extract(抽字段) → alert(规则) → 通知`

---

## 3. 配置：Config 模型为唯一权威

- 定义一个独立的 **`Config`（Go struct）** 作为唯一权威。
- **YAML（当前入口）** 和未来的 **WebUI（表单）** 都只是映射到同一个 `Config` 的两个入口。
- 因此"YAML → WebUI"不是重写，只是加一个入口，引擎与配置模型不变。

M 配置形态：

```yaml
graph:
  - id: serial        # 1. source（实时串口）
    kind: source
    type: serial
    port: /dev/ttyUSB0
    baud: 115200

  - id: g             # 2. filter（缩小日志）
    kind: filter
    type: grep
    match: "MEM|alloc|free"
    from: serial

  - id: s             # 3. process（外部脚本加工）
    kind: process
    type: script
    cmd: python3 ./mem_check.py
    runtime: python3>=3.8   # 提示用，不强制/自动装
    from: g

  - id: out           # 4. sink（输出）
    kind: sink
    type: console
    from: s
```

边 = `from` 字段；同一 id 可被多个下游引用（扇出）。

---

## 4. 架构分层（WebUI 只是未来新 driver）

| 层 | 职责 | 跨版本不变 |
|---|---|---|
| Config | 唯一权威配置模型 | ✅ |
| 编排格式 | YAML（本版）/ 未来 WebUI | 执行入口之一 |
| DAG 引擎 | `pkg/dag` 独立库 | ✅ 复用在任何 driver |
| 插件 | grep/script/alert 等 | ✅ |
| 前端 | CLI 打印（本版）→ Vue 3（未来） | 新增层 |

- **CLI 与服务只是同一 dag 库的两个 driver**：`run`（一次性批处理）/ `serve`（未来常驻 + WebUI 连接）。MVP 只做 `run`。
- 保持轻量：进程内 goroutine + channel，**不引入任何服务框架/RPC/分布式**。

---

## 5. 内置 vs 外部（权责划分）

**判别标准：工具是否需提供/维护运行时。**

- **能内置实现的 → 内置**（单二进制即用，零额外环境）：file/stdin/serial、grep/extract/agg/alert、console/file。编译进 Go 二进制。
- **要任意语言自由逻辑 → 外部 `script`**：工具只做**子进程通信壳**（spawn 进程、stdin 传 JSON Lines Item、读 stdout 结果）。运行时（python 等）由**执行机器自装**，工具只做友好的可用性提示，**不内置也不自动安装**。

> 原则：工具当壳，不背环境。常见功能内置（轻量即时用），冷门逻辑靠外部逃生舱（用户自担环境）。

---

## 6. 技术栈

- **语言**：Go。单二进制、跨平台、`embed` 可打包前端。
- **LLM**：HTTP 调 OpenAI 兼容接口，不绑 SDK。LLM 仅是**一个 node 插件**，非核心，后期实现。
- **WebUI**：Vue 3（以后版）。后端 `net/http` + `embed`，实时推送（WebSocket/SSE）以后定。
- **MVP 最小节点**：`source: file/stdin` + `filter: grep` + `sink: console/file`。

---

## 7. MVP 验收（第一个示例）

跑通 `file → grep → console/file`，验证三件事：

1. **EOS**：文件读完，各节点 Flush。
2. **扇出**：同一源可接多下游。
3. **背压**：慢下游不阻塞其它分支。

串口、script、alert、store、LLM 均作为 node 后续逐步加入。

---

## 附：Anti-Goals（不做的事）

- 不做分布式 / 常驻服务框架 / RPC（保持轻量本地）。
- 不做脚本运行时环境安装（Python 等由用户系统提供）。
- 不做面向多租户共享的调度平台（非 DolphinScheduler）。
- LLM 不参与告警判定，只做归因分析。