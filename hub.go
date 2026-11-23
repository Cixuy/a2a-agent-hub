package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- 注册表 ----------

type Agent struct {
	Card     AgentCard
	URL      string
	Healthy  bool
	LastSeen time.Time
	Quota    *bucket

	mu sync.Mutex // 只保护下面两个字段：每次调用都会改写，而 /healthz 会被并发轮询
}

func (a *Agent) healthy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Healthy
}

func (a *Agent) setHealthy(ok bool) {
	a.mu.Lock()
	a.Healthy = ok
	if ok {
		a.LastSeen = time.Now()
	}
	a.mu.Unlock()
}

type Registry struct {
	mu     sync.RWMutex
	agents []*Agent
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) Add(a *Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, x := range r.agents {
		if x.URL == a.URL {
			r.agents[i] = a
			return
		}
	}
	r.agents = append(r.agents, a)
}

func (r *Registry) All() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Agent, len(r.agents))
	copy(out, r.agents)
	return out
}

func (r *Registry) ByName(name string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.agents {
		if a.Card.Name == name {
			return a
		}
	}
	return nil
}

// FetchCard 从对方的 /.well-known/agent-card.json 拉名片，这是 A2A 的发现入口。
func FetchCard(base string) (*AgentCard, error) {
	url := strings.TrimRight(base, "/") + "/.well-known/agent-card.json"
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("card http %d", resp.StatusCode)
	}
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, err
	}
	return &card, nil
}

// ---------- 限额 ----------

type bucket struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	used   int
	reset  time.Time
}

func newBucket(limit int, window time.Duration) *bucket {
	return &bucket{limit: limit, window: window, reset: time.Now().Add(window)}
}

func (b *bucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.After(b.reset) {
		b.used = 0
		b.reset = now.Add(b.window)
	}
	if b.used >= b.limit {
		return false
	}
	b.used++
	return true
}

// ---------- 任务存储 ----------

type Hub struct {
	mu         sync.RWMutex
	reg        *Registry
	tasks      map[string]*Task
	byMsgID    map[string]string
	subs       map[string][]chan Task
	storePath  string
	saveCh     chan struct{}
	HopTimeout time.Duration
	MaxRetry   int
}

func NewHub(reg *Registry, storePath string) *Hub {
	h := &Hub{
		reg:        reg,
		tasks:      map[string]*Task{},
		byMsgID:    map[string]string{},
		subs:       map[string][]chan Task{},
		storePath:  storePath,
		saveCh:     make(chan struct{}, 1),
		HopTimeout: 1200 * time.Millisecond,
		MaxRetry:   1,
	}
	h.load()
	go h.saver()
	return h
}

func now() string { return time.Now().Format(time.RFC3339) }

func (h *Hub) load() {
	data, err := readFile(h.storePath)
	if err != nil {
		return
	}
	var dump struct {
		Tasks   map[string]*Task `json:"tasks"`
		ByMsgID map[string]string `json:"byMsgId"`
	}
	if json.Unmarshal(data, &dump) != nil {
		return
	}
	if dump.Tasks != nil {
		h.tasks = dump.Tasks
	}
	if dump.ByMsgID != nil {
		h.byMsgID = dump.ByMsgID
	}
	// 重启时把中断的任务标成可续跑：状态留在原处，调用方能看到断点
	for _, t := range h.tasks {
		if t.Status.State == "working" {
			t.Status.State = "working"
			t.Status.Message = &Message{Role: "agent", Parts: []Part{{Kind: "text", Text: "网关重启，任务已从断点恢复"}}}
		}
	}
}

func (h *Hub) touch() {
	select {
	case h.saveCh <- struct{}{}:
	default:
	}
}

func (h *Hub) saver() {
	for range h.saveCh {
		time.Sleep(80 * time.Millisecond)
		h.mu.RLock()
		dump := struct {
			Tasks   map[string]*Task `json:"tasks"`
			ByMsgID map[string]string `json:"byMsgId"`
		}{h.tasks, h.byMsgID}
		data, _ := json.MarshalIndent(dump, "", "  ")
		h.mu.RUnlock()
		_ = writeFile(h.storePath, data)
	}
}

func (h *Hub) Get(id string) *Task {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.tasks[id]
}

// clone 给 HTTP / SSE 侧一份可安全序列化的快照（切片一起复制），
// 否则 json 边编码、worker 边 append Hops / 改 Status。
func (t *Task) clone() *Task {
	d := *t
	d.Artifacts = append([]Artifact(nil), t.Artifacts...)
	d.History = append([]Message(nil), t.History...)
	d.Hops = append([]Hop(nil), t.Hops...)
	if t.Status.Message != nil {
		m := *t.Status.Message
		m.Parts = append([]Part(nil), t.Status.Message.Parts...)
		d.Status.Message = &m
	}
	return &d
}

// Snapshot 是 Get 的只读版本，拿到的副本不会被后台改动，适合直接 json 出去。
func (h *Hub) Snapshot(id string) *Task {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if t, ok := h.tasks[id]; ok {
		return t.clone()
	}
	return nil
}

func (h *Hub) ByMessageID(mid string) *Task {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if id, ok := h.byMsgID[mid]; ok {
		return h.tasks[id]
	}
	return nil
}

func (h *Hub) List() []*Task {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Task, 0, len(h.tasks))
	for _, t := range h.tasks {
		out = append(out, t.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Subscribe 给 SSE 用：任务每变一次，推一份快照
func (h *Hub) Subscribe(id string) chan Task {
	ch := make(chan Task, 8)
	h.mu.Lock()
	h.subs[id] = append(h.subs[id], ch)
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(id string, ch chan Task) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.subs[id]
	for i, c := range list {
		if c == ch {
			h.subs[id] = append(list[:i], list[i+1:]...)
			break
		}
	}
	close(ch)
}

func (h *Hub) publish(t *Task) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs[t.ID] {
		select {
		case ch <- *t.clone():
		default:
		}
	}
}

func (h *Hub) setState(t *Task, state, note string) {
	h.mu.Lock()
	t.Status.State = state
	t.Status.Timestamp = now()
	t.UpdatedAt = now()
	if note != "" {
		t.Status.Message = &Message{Role: "agent", Parts: []Part{{Kind: "text", Text: note}}}
	}
	h.mu.Unlock()
	h.touch()
	h.publish(t)
}

// ---------- 编排 ----------

var intentRules = []struct {
	name  string
	words []string
	chain []string
}{
	{"device", []string{"打印", "投屏", "网络", "vpn", "断网", "连不上", "慢", "卡", "设备", "wi-fi", "wifi"}, []string{"设备诊断", "知识库"}},
	{"account", []string{"账号", "密码", "权限", "登录", "验证码", "被锁", "开通"}, []string{"知识库", "工单"}},
	{"ticket", []string{"报修", "建单", "工单", "派单", "进度"}, []string{"工单"}},
}

func planFor(text string) []string {
	lower := strings.ToLower(text)
	for _, r := range intentRules {
		for _, w := range r.words {
			if strings.Contains(lower, w) {
				return r.chain
			}
		}
	}
	return []string{"知识库"}
}

// Send 是 message/send 的落点：建任务、按意图编排、逐跳调用下游 agent。
// 幂等必须在同一把锁里完成"查 byMsgID + 建任务 + 登记 byMsgID"：
// 以前先加读锁查、解锁后再加写锁插入，两个同 messageId 的并发请求会双双查空，
// 各建一个任务，下游 agent 被调两遍（配额双扣、工单重复）。
func (h *Hub) Send(mid string, msg Message, ctxID string) (*Task, bool) {
	text := ""
	for _, p := range msg.Parts {
		if p.Kind == "text" {
			text += p.Text
		}
	}
	t := &Task{
		ID:        "task-" + shortID(),
		ContextID: ctxID,
		Status:    TaskStatus{State: "submitted", Timestamp: now()},
		History:   []Message{msg},
		CreatedAt: now(),
		UpdatedAt: now(),
		MessageID: mid,
	}
	h.mu.Lock()
	if mid != "" {
		if id, ok := h.byMsgID[mid]; ok {
			if old := h.tasks[id]; old != nil {
				h.mu.Unlock()
				return old, true // 幂等：同一个 messageId 不重复执行
			}
		}
	}
	h.tasks[t.ID] = t
	if mid != "" {
		h.byMsgID[mid] = t.ID
	}
	h.mu.Unlock()
	h.touch()
	h.publish(t)

	go h.run(t, text)
	return t, false
}

func (h *Hub) run(t *Task, text string) {
	defer func() {
		if r := recover(); r != nil {
			h.setState(t, "failed", fmt.Sprintf("编排异常：%v", r))
		}
	}()
	h.setState(t, "working", "已受理，开始编排")
	chain := planFor(text)
	seq := 0
	failed := 0

	for _, skillWant := range chain {
		agent := h.pickAgent(skillWant)
		seq++
		hop := Hop{Seq: seq, Agent: "-", Skill: skillWant, State: "failed", Attempt: 1}
		if agent == nil {
			hop.Note = "没有匹配到这个能力的 agent"
			h.appendHop(t, hop)
			failed++
			continue
		}
		hop.Agent = agent.Card.Name

		start := time.Now()
		art, err := h.callAgent(agent, t, text)
		hop.Ms = time.Since(start).Milliseconds()

		if err != nil {
			// 失败重试一次；限额拒绝不重试
			if qerr, ok := err.(quotaError); ok {
				hop.State = "rejected"
				hop.Note = string(qerr)
				h.appendHop(t, hop)
				failed++
				continue
			}
			hop.Attempt = 2
			start = time.Now()
			art, err = h.callAgent(agent, t, text)
			hop.Ms = time.Since(start).Milliseconds()
		}

		if err != nil {
			hop.Note = err.Error()
			h.appendHop(t, hop)
			failed++
			continue
		}

		hop.State = "ok"
		for _, a := range art {
			hop.Artifact = append(hop.Artifact, a.Name)
		}
		h.appendHop(t, hop)

		h.mu.Lock()
		t.Artifacts = append(t.Artifacts, art...)
		t.History = append(t.History, Message{
			Role:  "agent",
			Parts: []Part{{Kind: "text", Text: agent.Card.Name + " 完成：" + art[0].Name}},
		})
		h.mu.Unlock()
		h.touch()
		h.publish(t)
	}

	if failed == len(chain) {
		h.setState(t, "failed", "所有下游 agent 都没能完成")
		return
	}
	note := "已完成"
	if failed > 0 {
		note = fmt.Sprintf("部分降级完成（%d/%d 跳失败），结论仅基于成功的那部分", failed, len(chain))
	}
	h.setState(t, "completed", note)
}

func (h *Hub) appendHop(t *Task, hop Hop) {
	h.mu.Lock()
	t.Hops = append(t.Hops, hop)
	t.UpdatedAt = now()
	h.mu.Unlock()
	h.touch()
	h.publish(t)
}

func (h *Hub) pickAgent(skill string) *Agent {
	for _, a := range h.reg.All() {
		for _, s := range a.Card.Skills {
			if s.Name == skill || s.ID == skill {
				return a
			}
		}
	}
	return nil
}

type quotaError string

func (e quotaError) Error() string { return string(e) }

func (h *Hub) callAgent(a *Agent, t *Task, text string) ([]Artifact, error) {
	if a.Quota != nil && !a.Quota.take() {
		return nil, quotaError("超出该 agent 的调用配额，已拒绝并跳过")
	}

	body := Message{
		Role: "user",
		Parts: []Part{
			{Kind: "text", Text: text},
			{Kind: "data", Data: map[string]any{"history": len(t.History), "taskId": t.ID}},
		},
	}
	req := RPCRequest{JSONRPC: "2.0", ID: t.ID, Method: "message/send"}
	params, _ := json.Marshal(SendParams{Message: body, MessageID: t.ID, ContextID: t.ContextID})
	req.Params = params
	raw, _ := json.Marshal(req)

	client := &http.Client{Timeout: h.HopTimeout}
	resp, err := client.Post(strings.TrimRight(a.URL, "/")+"/a2a", "application/json", bytes.NewReader(raw))
	if err != nil {
		a.setHealthy(false)
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		a.setHealthy(false)
		return nil, fmt.Errorf("agent http %d: %s", resp.StatusCode, truncate(string(data), 120))
	}
	var rpc RPCResponse
	if err := json.Unmarshal(data, &rpc); err != nil {
		return nil, fmt.Errorf("解析下游响应失败")
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("%s", rpc.Error.Message)
	}
	var out struct {
		Artifacts []Artifact `json:"artifacts"`
	}
	if err := json.Unmarshal(rpc.Result, &out); err != nil {
		return nil, fmt.Errorf("下游返回结构不符合 A2A")
	}
	if len(out.Artifacts) == 0 {
		return nil, fmt.Errorf("下游没有返回结果")
	}
	a.setHealthy(true)
	return out.Artifacts, nil
}
