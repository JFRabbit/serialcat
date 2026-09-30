# logdiag 使用指南

> 面向**使用用户**。这里讲"怎么用"，不深入技术细节；想看架构与内部设计见根目录 `README.md`。

logdiag 是一个轻量的本地日志处理工具。最常见的几个用途：

- 实时看**串口日志**（尤其嵌入式设备的 `logdiag agent` 串口流）。
- 从大日志里**筛出关心的行**，打印或存文件。
- 向串口设备**下发命令**，把设备的串口想成"远程可操作的终端"。

---

## 1. 安装

到发布目录下载对应的二进制放到一起即可。命令名：**`logdiag`**。

不同平台/架构对应不同的文件，例如 Windows 用 `logdiag.exe`，macOS（Intel）用 `logdiag`（darwin/amd64）。

> 说明：logdiag 本身在 Windows/macOS/Linux 都能运行查看远端串口；但"直接连本机串口"的能力（`agent` 与本地写）**目前仅限 Windows**。

验证安装：

```bash
logdiag version
```

---

## 2. 核心概念：配置文件（YAML）

logdiag 按"一条日志流水怎么走"来工作，这个流水叫 **DAG**，写在一个 YAML 文件里，用 `-f` 指定：

```bash
logdiag run -f 你的配置.yaml
```

一个配置描述了几种"节点"和它们之间的连接关系：

| 名词 | 作用 | 例子 |
|---|---|---|
| source | 日志**从哪里来** | 读文件 / 拉串口流 / 连本机串口 |
| filter | 按规则**筛掉**不关心的行 | grep 正则过滤 |
| process | 交给**外部脚本**加工 | 插件 |
| sink | 日志**到哪里去** | 打印到屏幕 / 写入文件 |

一个最简单的例子 `file → console`（读文件并打印）：

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

要点：

- `id` 是节点名字，自己起，图里唯一。
- `from: [...]` 表示"这个节点吃谁的输出"。sink 一定要有 `from`。
- 一个节点可以被多个下游引用（比如同一个日志既打印又存盘）。

---

## 3. 常用场景

### 3.1 从大日志里筛关键词

```yaml
graph:
  - id: src
    kind: source
    type: file
    path: big_log.txt

  - id: err
    kind: filter
    type: grep
    match: "fail|null|error"      # 正则，命中才往下走
    from: [src]

  - id: out
    kind: sink
    type: console
    from: [err]
```

先校验配置无误再跑：

```bash
logdiag verify -f big_log.yaml    # 只看配置对不对
logdiag run -f big_log.yaml       # 跑起来
```

> `verify` 只做检查不执行，建议改完配置先 verify 一遍。

### 3.2 看远端串口日志（最常用）

串口物理插在 **Windows** 机器上，先在那跑 agent：

```bash
logdiag agent serve --port COM5 --baud 115200 --listen :7100
```

启动后它会打印本机可访问的地址（形如 `http://192.168.1.x:7100`），记下来。

回到看日志的电脑，配置里用 `serial-remote` 节点去拉：

```yaml
graph:
  - id: ser
    kind: source
    type: serial-remote
    url: "http://192.168.1.x:7100"   # agent 打印的地址

  - id: out
    kind: sink
    type: console
    from: [ser]
```

然后：

```bash
logdiag run -f serial.yaml
```

串口日志就会实时刷到屏幕上。断线会自动重连并有"断档"提示。

> 想在 agent 那台机器自己也留一份日志？agent 默认就落盘到运行目录 `logs/`，无需配置。

### 3.3 向串口设备下发命令

有**两种**方式，取决于串口被谁占用。

**方式 A（推荐，能看设备回显）**：串口由 `run`/`agent` 持有，logdiag 通过 HTTP 通道下发命令，设备的回显会沿登录流返回。

- 持有着开启写通道：
  - agent：`agent serve --enable-write`（会打印一个 token）
  - 本地：`logdiag run --enable-write -f 配置.yaml`
- 下发命令（默认发本机写通道 `127.0.0.1:7100`；写远程就加 `--url` 和 `--token`）：

```bash
logdiag send "ls -al"
logdiag send "AT+OPEN"
logdiag send --url http://192.168.1.x:7100 --token <token> "reboot"
```

**方式 B（裸发）**：串口空闲独占时，直接指定本机串口盲发，发完即退、看不到回显：

```bash
logdiag send --port COM5 "ls"
logdiag send --port COM5 --cmd "echo hi" arg1     # --cmd 与位置参数会拼接
```

> 细节：命令默认会自动补一个回车 `\r` 触发执行；末尾已带 `\r`/`\n` 则不重复补。想精确发字节用 `--raw`。

---

## 4. 其余命令一览

| 命令 | 用途 |
|---|---|
| `logdiag run -f x.yaml` | 执行一套 DAG 配置流水 |
| `logdiag verify -f x.yaml` | 只校验配置是否正确，不运行 |
| `logdiag version [--short]` | 打印版本号 |
| `logdiag update [--check]` | 检查并更新到最新版本 |
| `logdiag agent serve ...` | 在 Windows 开串口代理 |
| `logdiag agent probe [port]` | 列出本机串口 / 探测波特率 |
| `logdiag send ...` | 向串口下发一条命令 |

`logdiag -h` 或 `logdiag <命令> -h` 可以随时看帮助。

---

## 5. 常见问题

- **`serial port busy`**：串口被另一个程序（别的 agent、SecureCRT 等）占用了。关掉占用者，或改用"持有者下发"（`--url`）方式。
- **下发了命令但没反应**：设备一般要等待回车符才执行。若命令末尾没有 `\r`，试试明确带回车，如 `send "ls -al\r"`。
- **串口有持续日志，`send --port` 不好用**：这时命令回显会混在刷屏日志里，建议改用持有者 + `--url` 方式下发，回显在 run/agent 的日志里看。

---

> 更多内部机制、演进计划与已知限制，参见 `docs/roadmap.md` 与 `docs/limitation.md`。