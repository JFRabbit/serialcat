---
name: serialcat-agent
description: >-
  通过 serialcat agent 的 HTTP 接口实时拉取串口日志(/api/stream)、回查历史
  (/api/logs 分页+过滤+尾部快照)以及向串口设备下发命令(/api/write)。
  当 AI 需要读取某台插着串口设备的 Windows 机器上的日志、持续监控串口输出、
  或排查过去某段时间的串口数据时使用。
---

# SKILL — 让 AI 通过 serialcat agent 访问串口日志

> 这份文档是给 **AI 协作者 / code agent** 看的"怎么用 serialcat agent"。
> 面向使用/集成,不面向二次开发(架构看根 README.md)。

## 1. 是什么

`serialcat` 是一个轻量本地日志工具。其中 **agent 模式**跑在一台**插着串口设备的 Windows 机器**上,
把串口数据通过网络暴露出来,让**另一端**(Mac / 另一台机器 / AI)通过 HTTP 拉取:

- **实时看** → `/api/stream`(长连接流)
- **回看历史** → `/api/logs`(读 agent 默认落盘的全量日志,分页/过滤/尾部快照)
- **下发命令** → `/api/write`(需 agent 开了写开关)

对 AI 的意义:一次接入这个 agent,**实时盯 + 事后回看**串口日志都是同一套 HTTP,结构规整、容易消费。

## 2. 前置:拿到 agent 的连接信息

在你调用之前,需要现场提供(或问用户)：

- 运行 agent 机器的 **IP + 端口**(默认 `:7100`,即 `http://<ip>:7100`)
- 是否启用了**写开关 `--enable-write`** 以及**写令牌 `--token`**(写命令才需要)

**重要边界**
- agent 目前**仅 Windows 支持**(需真实串口);你所在的 mac/Linux 只能作为"消费端"拉取。
- agent 会**自动选串口、端口被占自动 +1**;打印的本机可访问 IP 里挑可达的那个。
- agent 默认把串口流**全量落盘**到本地 `logs/serialcat-serial-*.log`(逐行 flush、带毫秒时间戳)。
  历史接口 `/api/logs` 就是读这份落盘,你**不用关心文件名**,接口会帮你定位。

## 3. 探测与状态

```
# 存活
curl -s http://<ip>:7100/health            # → ok

# 枚举串口(JSON 数组)
curl -s http://<ip>:7100/api/ports
# → ["COM3","COM5"]

# 探测某串口可用波特率(可选)
curl -s "http://<ip>:7100/api/probe?port=COM5"
```

## 4. 实时日志(get 最新,持续跟)

`/api/stream` 是 **NDJSON 长连接流**:

```
curl -N "http://<ip>:7100/api/stream"
```

- 每行一个 JSON 帧,`Content-Type: application/x-ndjson`,agent 边读边 flush。
- 帧主要含 **时间戳 `ts`** + **原始文本 `text`**(可能还带 `src`/`meta` 等元信息)。
- **网络中断时流会断开**:稳健的消费方应捕获断流后自动重连 `curl -N`,并容忍"断线期间的数据断层"。

## 5. 历史日志(回看过去,分页 + 过滤)

`/api/logs` 读 agent 落盘的全量历史,以**分页 JSON**返回。两种取法:

### 5.1 尾部快照(只要最近 N 条)

```
curl -s "http://<ip>:7100/api/logs?tail=true&limit=100"
```

`tail=true` 一次性返回最近的 `limit` 条(时间正序),不可与 `after/until` 同用。

### 5.2 游标续读(从头/从某个时间点按页拉全)

```
curl -s "http://<ip>:7100/api/logs?after=2026-10-08%2011:41:21.365&limit=100"
```

**响应结构**

```
{
  "count": 100,                 // 本批实际返回行数
  "hasMore": true,              // false = 已读到末尾/until,全部拉完
  "next": "2026-10-08 11:41:24.512",  // 仅 hasMore 时非空,下次填 after 用
  "rows": [
    { "ts": "2026-10-08 11:41:21.365", "text": "<原始日志原文(可能含设备格式/ANSI)>" }
  ]
}
```

**完整拉取的循环语义**:客户端只要 `while (hasMore)` 就把 `next` 填进下一次 `after`,直到 `hasMore=false`。

```
after=""
for {
  resp = GET /api/logs?after=<after>&limit=100&...
  for row in resp.rows: output(row)
  if !resp.hasMore: break
  after = resp.next
}
```

**参数表**

| 参数 | 含义 | 默认 / 上限 |
|---|---|---|
| `after` | 只返回 `ts > after` 的行(游标续读) | 无 = 从文件头 |
| `until` | 只返回 `ts <= until` 的行 | 无 = 到文件尾 |
| `limit` | 本批条数上限 | 1000 / 最多 10000 |
| `filter` | 单层正则,只返回匹配行(去噪音) | 无 = 全量 |
| `tail` | 设为 `true` 走尾部快照 | 默认 false |

- **时间戳格式**: `2006-01-02 15:04:05.000`(秒.毫秒)。`after/until` 与 `ts` 同格式。
  因为 URL 里含**空格和冒号**,最好用 `url.Values` 编码或把空格写成 `%20`。
- **正则**: Go 的 `regexp` 语法(与 `filter:grep` 节点一致)。例 `filter=fail|error`。
- **错误**: 没开落盘/文件不存在 → `404`;`after>until`、`tail` 加了 `after`、`limit` 超上限、时间格式非法、正则编译失败 → `400`。

## 6. 下发命令(往串口写数据)

```
# 需要 agent 用 --enable-write 启动;若设置了 --token 需带 Authorization
curl -s -X POST \
  -H "Authorization: Bearer <token>" \
  "http://<ip>:7100/api/write?data=<urlencoded 命令>"
```

- 未开写开关 → `403`(先让 agent 加 `--enable-write` 重启)。
- 设置了 token 但没带/带错 → `401`。
- `data` 支持字面转义 `\r` `\n` `\t`(会被替换为真实控制字符),发送侧统一幂等处理。
- 成功 → `{"ok":"sent"}`。
- **单工注意**:串口是**单向流**,这里的写**不会返回设备回显**(`?data=...` 只管发)。
  要看设备回复,走 `/api/stream` 或 `/api/logs`,两者是同一份数据。

## 7. 给 AI 的分步工作流(推荐)

1. 先 `GET /health` 确认可达;不通就先要 IP/端口。
2. 要**实时监控**→ `curl -N /api/stream`,逐行消费,断流自动重连。
3. 要**排查过去一段时间**→ `/api/logs` + `after/until/filter` 分页拉,`while(hasMore)` 用 `next` 续读到完。
4. 要**只关注报错**→ 加 `&filter=fail|error|assert` 先缩小量。
5. 要**下发固件/调试命令**→ 确认 `--enable-write` 开了,拿 token,`POST /api/write`。
6. 要把串口拉进 serialcat 自己的 DAG 处理 → 配置里用 `source:serial-remote` 节点(见 `docs/usage.md`)。

## 8. 常见坑

- **日志是原始透传的**:`text` 里可能带设备的 ANSI 转义 / 内部时间戳,接口不加工,别指望它干净。
- **落盘从本次启动开始**:重启 agent 后 `/api/logs` 只读新文件,旧启动的历史不在此接口范围内。
- **同毫秒多行**:游标 `next` 按 `ts > after` 严格续读,极端"同毫秒行数超 limit"可能漏一撮,当前数据量下基本遇不到。
- **端口被自动 +1**:告诉你的端口可能已被占用偏移,以 agent 实际打印的为准。