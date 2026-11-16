package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// 三个下游 agent 都跑在独立端口上，网关通过 HTTP JSON-RPC 调它们，
// 不是进程内函数调用 —— 否则 A2A 就只是个名词。

func startAgent(card AgentCard, handler func(text string) []Artifact) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	})
	mux.HandleFunc("/a2a", func(w http.ResponseWriter, r *http.Request) {
		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPCError(w, nil, -32700, "解析失败", nil)
			return
		}
		switch req.Method {
		case "message/send":
			var p SendParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				writeRPCError(w, req.ID, -32602, "参数不合法", nil)
				return
			}
			text := ""
			for _, part := range p.Message.Parts {
				if part.Kind == "text" {
					text += part.Text
				}
			}
			arts := handler(text)
			writeRPCResult(w, req.ID, map[string]any{
				"id":        p.MessageID,
				"status":    map[string]any{"state": "completed", "timestamp": now()},
				"artifacts": arts,
			})
		default:
			writeRPCError(w, req.ID, -32601, "不支持的方法："+req.Method, nil)
		}
	})
	listenAddr := strings.TrimPrefix(strings.TrimPrefix(card.URL, "http://"), "https://")
	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go srv.ListenAndServe()
	return srv
}

func writeRPCResult(w http.ResponseWriter, id any, result any) {
	raw, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(RPCResponse{JSONRPC: "2.0", ID: id, Result: raw})
}

func writeRPCError(w http.ResponseWriter, id any, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg, Data: data}})
}

func addr(port string) string { return "127.0.0.1:" + port }

// ---------- 设备诊断 agent ----------

type symptom struct {
	keys   []string
	title  string
	cause  string
	metric [][2]string
	fix    []string
}

var symptoms = []symptom{
	{
		keys:  []string{"打印", "print"},
		title: "打印队列堆积",
		cause: "打印后台服务(Spooler)卡死，队列中积压任务未释放",
		metric: [][2]string{{"打印队列长度", "12 个"}, {"Spooler 服务状态", "STOPPED"},
			{"最近一次成功打印", "3 小时前"}, {"驱动版本", "v3.2（服务端 v4.1，不匹配）"}},
		fix: []string{"停掉 Spooler 并清空 C:\\Windows\\System32\\spool\\PRINTERS 下的残留文件",
			"驱动升级到 v4.1 后重新添加打印机", "队列清空后先打一张测试页验证"},
	},
	{
		keys:  []string{"投屏", "投影", "hdmi", "白屏"},
		title: "会议室投屏无信号",
		cause: "无线投屏器固件与主机显卡输出分辨率不匹配，握手失败",
		metric: [][2]string{{"投屏器在线", "是"}, {"协商分辨率", "3840x2160@60"},
			{"主机输出", "1920x1080@60"}, {"握手重试次数", "5"}},
		fix: []string{"把主机输出固定为 1920x1080@60 再重新连接",
			"投屏器固件升级到 2.4.1（修复 4K 协商回退）", "仍失败则改用 HDMI 直连并报修"},
	},
	{
		keys:  []string{"vpn", "连不上", "远程", "断网"},
		title: "VPN 拨号失败",
		cause: "客户端证书过期（有效期 90 天，已过期 3 天）",
		metric: [][2]string{{"证书剩余有效期", "-3 天"}, {"网关可达", "是"}, {"认证阶段", "TLS 握手被拒"},
			{"同类报障(近 7 天)", "9 次"}},
		fix: []string{"在自助门户重新签发客户端证书并重新导入",
			"确认系统时间与 NTP 同步（证书校验依赖时间）"},
	},
	{
		keys:  []string{"慢", "卡", "卡顿", "加载"},
		title: "办公系统访问缓慢",
		cause: "出口带宽被大流量下载占满，QoS 未对办公网段限速",
		metric: [][2]string{{"出口带宽占用", "97%"}, {"Top 会话", "某主机 340Mbps"},
			{"应用层 RTT", "480ms"}, {"同楼层报障", "4 人"}},
		fix: []string{"对办公网段启用 QoS 限速，单会话上限 50Mbps",
			"定位大流量主机并通知", "观察 15 分钟确认 RTT 回落"},
	},
}

func diagnoseAgent(text string) []Artifact {
	time.Sleep(time.Duration(120+rand.Intn(160)) * time.Millisecond)
	lower := strings.ToLower(text)
	var hit *symptom
	for i := range symptoms {
		for _, k := range symptoms[i].keys {
			if strings.Contains(lower, k) {
				hit = &symptoms[i]
				break
			}
		}
		if hit != nil {
			break
		}
	}
	if hit == nil {
		return []Artifact{{
			Name:        "诊断结论",
			Description: "没有匹配到已知故障模式",
			Parts: []Part{{Kind: "text", Text: "没有命中已知故障模式，建议转人工或补充现象描述（设备名、报错原文、发生时间）。"}},
		}}
	}
	var sb strings.Builder
	sb.WriteString("【" + hit.title + "】\n根因判断：" + hit.cause + "\n证据：\n")
	for _, m := range hit.metric {
		sb.WriteString("  - " + m[0] + "：" + m[1] + "\n")
	}
	sb.WriteString("置信度：" + fmt.Sprintf("%d%%", 70+rand.Intn(25)))
	return []Artifact{{
		Name:        "诊断结论",
		Description: hit.title,
		Parts: []Part{{Kind: "text", Text: sb.String()},
			{Kind: "data", Data: map[string]any{"cause": hit.cause, "fix": hit.fix}}},
	}}
}

// ---------- 知识库 agent ----------

type faq struct {
	keys   []string
	title  string
	body   string
	source string
}

var faqs = []faq{
	{[]string{"打印", "print"}, "打印机常见问题",
		"1) 队列积压：重启 Spooler 并清空 spool 目录；2) 驱动不匹配：统一升到 v4.1；3) 脱机：检查网络与 IP 是否被 DHCP 改动。", "IT 服务手册 §3.2"},
	{[]string{"投屏", "投影"}, "会议室投屏指引",
		"投屏器需与主机分辨率一致；4K 协商失败时手动锁 1080p；固件 2.4.1 修复了回退问题。", "会议室设备手册 §1.5"},
	{[]string{"vpn", "证书", "登录"}, "VPN 与证书自助",
		"客户端证书有效期 90 天，到期前 7 天会提醒；过期后在自助门户重新签发即可，无需停机。", "办公网接入规范 §4.1"},
	{[]string{"账号", "密码", "权限", "被锁"}, "账号与权限申请",
		"密码连续错误 5 次锁定 30 分钟，可在自助门户解锁；新增权限走 OA 审批，一般 1 个工作日内生效。", "账号管理办法 §2.3"},
	{[]string{"慢", "卡", "带宽"}, "办公网变慢的排查顺序",
		"先看出口占用（>85% 基本可判定拥塞），再看单主机大流量，最后查 DNS 与 RTT。", "网络运维手册 §6.1"},
	{[]string{"报修", "工单"}, "报修流程",
		"报修需提供：设备编号、位置、现象、发生时间；紧急件 30 分钟内响应，普通件 4 小时。", "服务台流程 §2.1"},
}

func knowledgeAgent(text string) []Artifact {
	time.Sleep(time.Duration(90+rand.Intn(120)) * time.Millisecond)
	lower := strings.ToLower(text)
	best := -1
	for i, f := range faqs {
		for _, k := range f.keys {
			if strings.Contains(lower, k) {
				best = i
				break
			}
		}
		if best >= 0 {
			break
		}
	}
	if best < 0 {
		return []Artifact{{Name: "知识条目", Description: "未命中",
			Parts: []Part{{Kind: "text", Text: "知识库里没有直接对应的条目。"}}}}
	}
	f := faqs[best]
	return []Artifact{{
		Name:        "知识条目",
		Description: f.title,
		Parts: []Part{
			{Kind: "text", Text: f.body},
			{Kind: "data", Data: map[string]any{"source": f.source}},
		},
	}}
}

// ---------- 工单 agent ----------

var ticketSeq = 4821

func ticketAgent(text string) []Artifact {
	time.Sleep(time.Duration(100+rand.Intn(150)) * time.Millisecond)
	ticketSeq++
	urgent := strings.Contains(text, "紧急") || strings.Contains(text, "全部") || strings.Contains(text, "无法")
	sla := "4 小时"
	if urgent {
		sla = "30 分钟"
	}
	id := fmt.Sprintf("INC-%d", ticketSeq)
	rec := map[string]any{"id": id, "text": text, "sla": sla, "createdAt": now()}
	appendTicket(rec)
	return []Artifact{{
		Name:        "工单",
		Description: id,
		Parts: []Part{
			{Kind: "text", Text: "已建单 " + id + "，SLA " + sla + "。报修内容：" + truncate(text, 60)},
			{Kind: "data", Data: rec},
		},
	}}
}
