# A2A 智能体协作网关

把一句话拆给多个专职 Agent 协作，并负责发现、路由、限额、幂等与审计。

解决的场景很具体：公司 IT 服务台每天收到"打印机坏了""VPN 连不上""账号被锁"这类报障。让一个大模型硬答，答不准也没人担责；拆成几个专职 Agent 各管一段，谁来分派、谁能调谁、出事怎么查，就是网关要解决的问题。

```
客户端 ──A2A(JSON-RPC + SSE)──▶ 网关
                                 │  注册表 / 能力路由 / 配额 / 任务状态机 / 审计
                    ┌────────────┼────────────┐
                    ▼            ▼            ▼
                 知识库       设备诊断        工单
              (:18091)       (:18092)       (:18093)
```

三个下游 Agent 各自独立进程、独立端口，通过 HTTP 上的 A2A 协议调用，不是进程内函数调用。

## 为什么是 A2A 而不是 MCP

- **MCP** 解决的是「一个 Agent 内部，模型怎么用工具」——工具是模型的手脚
- **A2A** 解决的是「Agent 与 Agent 之间怎么协作」——能力发现、任务生命周期、跨厂商互操作

两者不冲突：每个下游 Agent 内部完全可以用 MCP 去连自己的工具，网关这一层只关心"这个任务该交给谁、怎么交接、出错怎么兜"。

## 实现了 A2A 的哪几部分

| 能力 | 实现 |
|---|---|
| 能力发现 | 每个 Agent 暴露 `/.well-known/agent-card.json`，网关启动时拉名片注册 |
| 任务提交 | `POST /a2a` `message/send`，返回 Task（含 id / status / artifacts） |
| 任务查询 | `tasks/get` |
| 任务取消 | `tasks/cancel`（只对 submitted/working 生效；已结束的返回 `-32002` 并保留原状态） |
| 流式更新 | `GET /api/tasks/{id}/events`，SSE 推送任务快照 |

网关自己也是一张名片（`/.well-known/agent-card.json`），可以被更上层的 Agent 当普通 Agent 调。

## 网关这一层真正做的事

**能力路由**：按现象关键词判意图，决定调用链。设备类走「诊断 → 知识库」，账号类走「知识库 → 工单」，报修类直接建单。

**幂等**：同一个 `messageId` 重复提交，直接返回已有任务，不会重复执行、不会重复建单。索引落盘，重启后依然生效。并发同 `messageId` 也是安全的：查索引、建任务、登记索引在同一把锁内完成，不会出现两条都查空各建一单（并发 20 个同 `messageId` 只出 1 个任务，另 19 个 `duplicated=true`）。

**配额与降级**：每个 Agent 独立令牌桶（默认 60 次/分钟），超额直接拒绝并记一条 `rejected` 的跳，任务标为"部分降级完成"，而不是整体失败。单跳超时 1.2 秒，失败重试一次（配额拒绝不重试）。

**取消**：`tasks/cancel` 只在任务还没结束时生效（submitted / working / input-required），编排 goroutine 每跳前查一次状态，取消后不再调下游，剩下的跳不留下 hop。已经 completed / failed 的任务再取消返回 `-32002`，原状态保持不变——否则"取消"会把"已经做完了"抹成"被取消"。

**审计**：每次跨 Agent 调用记一条 hop：谁调的、哪个技能、耗时多少、第几次尝试、产出是什么。界面上的瀑布图就是这些数据。

**断点恢复**：任务状态每变一次就落盘（`data/tasks.json`，原子写）。进程被 `kill` 后用同样的命令重启，任务和幂等索引都还在。

## 跑起来

```bash
go build -o agenthub.exe .
./agenthub.exe
# 控制台 http://127.0.0.1:18090
```

无第三方依赖，只用了标准库。

试一试这句话：

```
打印机连不上，整个楼层的文件都打不出来
```

它会被判成设备类，先调「设备诊断」拿到根因（打印队列积压 / 驱动版本不匹配）和四条指标证据，再调「知识库」取处置步骤，两步串行，界面上能看到两条瀑布和两份产出。

## 实测

| 场景 | 结果 |
|---|---|
| 设备类报障 | 2 跳（诊断 157ms + 知识库 95ms），返回 2 个 artifact |
| 账号类报障 | 2 跳（知识库 + 工单），自动建单 INC-4822，SLA 4 小时 |
| 重复提交同一 messageId | 幂等命中，返回同一 taskId，未重复执行 |
| SSE 订阅 | 依次收到 `working` ×5 → `completed`，与状态机一致 |
| `POST /api/crash` 后重启 | 3 条任务全部恢复，幂等索引仍生效 |
| `GET /healthz` | 200 `{"status":"ok","agentsUp":3,"agentsTotal":3,"tasksTotal":5,"uptimeSec":6}` |

`/healthz` 是给进程管理器和巡检脚本用的：只回答「进程活着、几个下游是通的、任务积压多少」，
不落盘也不调下游，高频轮询不会拖慢主链路。**一个下游都不通时返回 503 + `status:degraded`** ——
网关接单也编排不动，这种状态让上游直接熔断比返回 200 更有用：

```bash
curl -s http://127.0.0.1:18090/healthz
```

并发正确性另有一条自检：任务详情、`/healthz`、SSE 都在读同一份状态，
用 race 检测器压一遍（边压 `/api/send` 边轮询 `/healthz` 与 `/api/tasks`）应为 0 条告警：

```bash
go build -race -o /tmp/ahr.exe . && /tmp/ahr.exe &
for i in $(seq 1 10); do curl -s -X POST http://127.0.0.1:18090/api/send \
  -d "{\"text\":\"vpn连不上\",\"messageId\":\"r-$i\"}" -o /dev/null & done
for i in $(seq 1 60); do curl -s http://127.0.0.1:18090/healthz -o /dev/null; done
```

## 目录

```
main.go      HTTP 入口、注册表初始化、A2A 与 REST 路由、SSE
hub.go       任务状态机、意图编排、跳级重试、配额、落盘
registry.go  名片拉取与注册（在 hub.go 内）
agents.go    三个下游 Agent 的实现与各自的 HTTP 服务
types.go     A2A 数据结构
ui.go        单页控制台（瀑布 + 产出）
data/        任务落盘（运行时生成）
```

## 已知局限

- 意图路由是关键词规则，不是语义理解；换到别的业务域要重写 `intentRules`
- 任务状态机只有 submitted/working/completed/failed/canceled，没做 `input-required` 的多轮澄清
- 没有做 Agent 之间的鉴权（注册即可调用），生产环境要加签名与白名单
- SSE 是单向广播，断线重连没有实现 `Last-Event-ID` 续传

下一步想补的是：注册表的准入校验（防能力声明造假）、按 Agent 的熔断（连续失败自动摘除）、以及把 trace 做成可回放（重跑某一跳做对比）。
