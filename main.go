package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func shortID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

func writeFile(p string, data []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func appendTicket(rec map[string]any) {
	const p = "tickets.json"
	var all []map[string]any
	if data, err := os.ReadFile(p); err == nil {
		json.Unmarshal(data, &all)
	}
	all = append(all, rec)
	if data, err := json.MarshalIndent(all, "", "  "); err == nil {
		os.WriteFile(p, data, 0o644)
	}
}

func mustDir(p string) {
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
}

// ---------- HTTP ----------

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	mustDir("data/tasks.json")

	hubAddr := addr("18090")
	knowledgeAddr := addr("18091")
	diagnoseAddr := addr("18092")
	ticketAddr := addr("18093")

	// 三个下游 agent，各自独立端口、独立名片
	startAgent(AgentCard{
		Name: "知识库", Description: "企业 IT 服务手册检索，回答流程与自助操作",
		URL: "http://" + knowledgeAddr, Version: "1.0.0", ProtocolVersion: "0.2.0",
		DefaultInputModes: []string{"text"}, DefaultOutputModes: []string{"text", "data"},
		Skills: []Skill{{ID: "kb", Name: "知识库", Description: "按现象检索手册条目", Tags: []string{"知识", "手册", "流程"}}},
	}, knowledgeAgent)

	startAgent(AgentCard{
		Name: "设备诊断", Description: "根据现象与设备指标判断故障根因",
		URL: "http://" + diagnoseAddr, Version: "1.0.0", ProtocolVersion: "0.2.0",
		DefaultInputModes: []string{"text"}, DefaultOutputModes: []string{"text", "data"},
		Skills: []Skill{{ID: "diag", Name: "设备诊断", Description: "故障根因判断并给证据", Tags: []string{"诊断", "根因", "指标"}}},
	}, diagnoseAgent)

	startAgent(AgentCard{
		Name: "工单", Description: "创建服务台工单并给出 SLA",
		URL: "http://" + ticketAddr, Version: "1.0.0", ProtocolVersion: "0.2.0",
		DefaultInputModes: []string{"text"}, DefaultOutputModes: []string{"text", "data"},
		Skills: []Skill{{ID: "ticket", Name: "工单", Description: "建单、定级、派发", Tags: []string{"工单", "报修"}}},
	}, ticketAgent)

	// 网关自己也是一张名片，可以被更上层的 agent 发现
	reg := NewRegistry()
	for _, base := range []string{"http://" + knowledgeAddr, "http://" + diagnoseAddr, "http://" + ticketAddr} {
		for i := 0; i < 20; i++ { // 等下游起来
			card, err := FetchCard(base)
			if err == nil {
				reg.Add(&Agent{Card: *card, URL: base, Healthy: true, LastSeen: time.Now(),
					Quota: newBucket(60, time.Minute)})
				break
			}
			time.Sleep(120 * time.Millisecond)
		}
	}

	hub := NewHub(reg, "data/tasks.json")
	startAt := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})

	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, AgentCard{
			Name: "IT 服务台网关", Description: "把一句话拆给多个专职 agent 协作，并做鉴权、限额与审计",
			URL: "http://" + hubAddr, Version: "1.0.0", ProtocolVersion: "0.2.0",
			DefaultInputModes: []string{"text"}, DefaultOutputModes: []string{"text", "data", "artifact"},
			Skills: []Skill{{ID: "helpdesk", Name: "服务台编排", Description: "按现象编排多个 agent", Tags: []string{"编排", "路由"}}},
		})
	})

	// 健康检查：只回答"进程活着、下游几个是通的、任务积压多少"，
	// 不做重活（不落盘、不调下游），保证被高频轮询也不会拖慢主链路
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		agents := reg.All()
		healthy := 0
		names := make([]string, 0, len(agents))
		for _, a := range agents {
			if a.healthy() {
				healthy++
			} else {
				names = append(names, a.Card.Name)
			}
		}
		tasks := hub.List()
		running := 0
		for _, t := range tasks {
			if t.Status.State == "working" {
				running++
			}
		}
		status := "ok"
		code := 200
		if healthy == 0 || len(agents) == 0 {
			status = "degraded" // 一个下游都不通，网关接单也没意义
			code = 503
		}
		jsonOut(w, code, map[string]any{
			"status":      status,
			"uptimeSec":   int(time.Since(startAt).Seconds()),
			"agentsTotal": len(agents),
			"agentsUp":    healthy,
			"agentsDown":  names,
			"tasksTotal":  len(tasks),
			"tasksRunning": running,
			"version":     "1.0.0",
		})
	})

	mux.HandleFunc("/api/agents", func(w http.ResponseWriter, r *http.Request) {
		type row struct {
			Name    string   `json:"name"`
			URL     string   `json:"url"`
			Skills  []string `json:"skills"`
			Healthy bool     `json:"healthy"`
			Desc    string   `json:"desc"`
		}
		var out []row
		for _, a := range reg.All() {
			var sk []string
			for _, s := range a.Card.Skills {
				sk = append(sk, s.Name)
			}
			out = append(out, row{a.Card.Name, a.URL, sk, a.healthy(), a.Card.Description})
		}
		jsonOut(w, 200, out)
	})

	mux.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		var in struct {
			Text      string `json:"text"`
			MessageID string `json:"messageId"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if strings.TrimSpace(in.Text) == "" {
			jsonOut(w, 400, map[string]string{"error": "内容为空"})
			return
		}
		t, dup := hub.Send(in.MessageID, Message{Role: "user", Parts: []Part{{Kind: "text", Text: in.Text}}}, "ctx-"+shortID())
		// t 是后台 d 编排 goroutine 还在改的对象，序列化要用快照
	jsonOut(w, 200, map[string]any{"task": hub.Snapshot(t.ID), "duplicated": dup})
	})

	// 演示用：把网关进程直接干掉，重启后任务还在（状态每步落盘）
	mux.HandleFunc("/api/crash", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]string{"status": "exiting"})
		go func() { time.Sleep(150 * time.Millisecond); os.Exit(1) }()
	})

	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, hub.List())
	})

	mux.HandleFunc("/api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		if strings.HasSuffix(rest, "/events") {
			id := strings.TrimSuffix(rest, "/events")
			sse(w, r, hub, id)
			return
		}
		if t := hub.Snapshot(rest); t != nil {
			jsonOut(w, 200, t)
			return
		}
		jsonOut(w, 404, map[string]string{"error": "任务不存在"})
	})

	// 网关自身的 A2A 入口：外部 agent / 客户端也能像调普通 agent 一样调它
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
			t, _ := hub.Send(p.MessageID, p.Message, p.ContextID)
			writeRPCResult(w, req.ID, hub.Snapshot(t.ID))
		case "tasks/get":
			var p struct {
				ID string `json:"id"`
			}
			json.Unmarshal(req.Params, &p)
			if t := hub.Snapshot(p.ID); t != nil {
				writeRPCResult(w, req.ID, t)
			} else {
				writeRPCError(w, req.ID, -32001, "任务不存在", nil)
			}
		case "tasks/cancel":
			var p struct {
				ID string `json:"id"`
			}
			json.Unmarshal(req.Params, &p)
			if t := hub.Get(p.ID); t != nil {
				hub.setState(t, "canceled", "调用方取消")
				writeRPCResult(w, req.ID, hub.Snapshot(p.ID))
			} else {
				writeRPCError(w, req.ID, -32001, "任务不存在", nil)
			}
		default:
			writeRPCError(w, req.ID, -32601, "不支持的方法："+req.Method, nil)
		}
	})

	fmt.Println("A2A Hub 已启动 http://" + hubAddr)
	fmt.Println("下游 agent: 知识库 " + knowledgeAddr + " / 设备诊断 " + diagnoseAddr + " / 工单 " + ticketAddr)
	if err := http.ListenAndServe(hubAddr, mux); err != nil {
		fmt.Println(err)
	}
}

func sse(w http.ResponseWriter, r *http.Request, hub *Hub, id string) {
	t := hub.Snapshot(id)
	if t == nil {
		http.Error(w, "no task", 404)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no sse", 500)
		return
	}
	send := func(t *Task) {
		b, _ := json.Marshal(t)
		fmt.Fprintf(w, "event: task\ndata: %s\n\n", b)
		fl.Flush()
	}
	send(t)
	ch := hub.Subscribe(id)
	defer hub.Unsubscribe(id, ch)
	timeout := time.After(20 * time.Second)
	for {
		select {
		case snap := <-ch:
			send(&snap)
		case <-timeout:
			fmt.Fprint(w, "event: end\ndata: {}\n\n")
			fl.Flush()
			return
		case <-r.Context().Done():
			return
		}
	}
}

var _ = io.EOF
