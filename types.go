package main

import "encoding/json"

// A2A 协议用到的类型。字段名按 A2A 规范来，方便被别的实现对接。

type Part struct {
	Kind string `json:"kind"` // text / data / file
	Text string `json:"text,omitempty"`
	Data any    `json:"data,omitempty"`
}

type Message struct {
	Role  string `json:"role"` // user / agent
	Parts []Part `json:"parts"`
}

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// AgentCard 是 A2A 的“名片”：一个 agent 会什么、怎么连、支持哪些输入输出模态。
type AgentCard struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	URL                string   `json:"url"`
	Version            string   `json:"version"`
	ProtocolVersion    string   `json:"protocolVersion"`
	DefaultInputModes  []string `json:"defaultInputModes"`
	DefaultOutputModes []string `json:"defaultOutputModes"`
	Skills             []Skill  `json:"skills"`
}

type TaskStatus struct {
	State     string   `json:"state"` // submitted / working / input-required / completed / canceled / failed
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp"`
}

type Artifact struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parts       []Part `json:"parts"`
}

type Task struct {
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Status    TaskStatus `json:"status"`
	Artifacts []Artifact `json:"artifacts"`
	History   []Message  `json:"history"`
	Hops      []Hop      `json:"hops"`
	CreatedAt string     `json:"createdAt"`
	UpdatedAt string     `json:"updatedAt"`
	MessageID string     `json:"-"`
}

// Hop 是网关自己的审计记录：一次跨 agent 调用就是一条。
type Hop struct {
	Seq      int      `json:"seq"`
	Agent    string   `json:"agent"`
	Skill    string   `json:"skill"`
	State    string   `json:"state"` // ok / failed / rejected
	Ms       int64    `json:"ms"`
	Attempt  int      `json:"attempt"`
	Note     string   `json:"note,omitempty"`
	Artifact []string `json:"artifacts,omitempty"`
}

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  jsonRawMessage  `json:"params"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// jsonRawMessage 只是给 json.RawMessage 起个短名字。
// 注意别写成 []byte：那样 params 会被当成 base64 字符串收发，
// 外部客户端发标准 JSON 对象时整个请求会以 -32700「解析失败」被拒。
type jsonRawMessage = json.RawMessage

type SendParams struct {
	Message   Message `json:"message"`
	MessageID string  `json:"messageId"`
	ContextID string  `json:"contextId,omitempty"`
}
