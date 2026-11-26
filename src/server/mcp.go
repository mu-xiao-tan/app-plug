package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	mcpLatest   = "2025-11-25"
	mcpLegacy   = "2025-03-26"
	mcpMaxBody  = 32 << 10
	mcpMaxBatch = 16
)

type mcpObject map[string]json.RawMessage

type mcpReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mcpFailure(id json.RawMessage, code int, message string) mcpReply {
	return mcpReply{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}}
}

func (s *routeStore) serveMCP(w http.ResponseWriter, r *http.Request, c *catalog, host string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	now := time.Now()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(now.Add(10 * time.Second))
	_ = controller.SetWriteDeadline(now.Add(20 * time.Second))
	if status, problem := c.policy.checkMCPHost(host); status != 0 {
		writeAdminResult(w, status, problem)
		return
	}
	for _, origin := range r.Header.Values("Origin") {
		if origin != "" {
			apiError(w, http.StatusForbidden, "MCP 不接受带 Origin 的请求")
			return
		}
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		apiError(w, http.StatusMethodNotAllowed, "MCP 仅接受 POST，不提供 SSE")
		return
	}
	if _, _, ok := c.policy.authenticate(r); !ok {
		writeAdminResult(w, http.StatusUnauthorized, authProblem(http.StatusUnauthorized))
		return
	}

	protocol := mcpLegacy // 无状态服务在没有版本头时按传输规范兼容旧版。
	if values := r.Header.Values("MCP-Protocol-Version"); len(values) != 0 {
		if len(values) != 1 || !mcpSupportsVersion(values[0]) {
			apiError(w, http.StatusBadRequest, "MCP-Protocol-Version 无效或不受支持")
			return
		}
		protocol = values[0]
	}
	if !mcpAcceptsJSON(r.Header.Values("Accept")) {
		apiError(w, http.StatusNotAcceptable, "Accept 须同时包含 application/json 和 text/event-stream")
		return
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if len(r.Header.Values("Content-Type")) != 1 || err != nil || media != "application/json" ||
		(params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		apiError(w, http.StatusUnsupportedMediaType, "请使用 UTF-8 编码的 application/json")
		return
	}

	ctx, cancel := context.WithDeadline(r.Context(), now.Add(15*time.Second))
	defer cancel()
	r = r.WithContext(ctx)
	if r.ContentLength > mcpMaxBody {
		apiError(w, http.StatusRequestEntityTooLarge, "MCP 请求体不得超过 32 KiB")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxBody))
	if err != nil {
		var size *http.MaxBytesError
		var timeout interface{ Timeout() bool }
		switch {
		case errors.As(err, &size):
			apiError(w, http.StatusRequestEntityTooLarge, "MCP 请求体不得超过 32 KiB")
		case errors.As(err, &timeout) && timeout.Timeout():
			apiError(w, http.StatusRequestTimeout, "读取 MCP 请求体超时")
		default:
			apiError(w, http.StatusBadRequest, "无法读取 MCP 请求体")
		}
		return
	}
	if !utf8.Valid(body) {
		writeJSON(w, http.StatusBadRequest, mcpFailure(nil, -32700, "请求体不是有效的 UTF-8 JSON"))
		return
	}
	body = bytes.TrimLeft(body, " \t\r\n")
	if len(body) != 0 && body[0] == '[' && protocol == mcpLegacy {
		s.serveMCPBatch(w, r, host, body)
		return
	}
	reply, status := s.dispatchMCP(r, host, body, false)
	if reply.JSONRPC == "" {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, reply)
}

func mcpSupportsVersion(protocol string) bool {
	return protocol == mcpLatest || protocol == "2025-06-18" || protocol == mcpLegacy
}

func mcpAcceptsJSON(headers []string) bool {
	var jsonOK, streamOK bool
	for _, header := range headers {
		for entry := range strings.SplitSeq(header, ",") {
			media, params, err := mime.ParseMediaType(entry)
			if err != nil {
				continue
			}
			if q, exists := params["q"]; exists {
				quality, err := strconv.ParseFloat(q, 64)
				if err != nil || !(quality > 0 && quality <= 1) {
					continue
				}
			}
			switch media {
			case "application/json":
				jsonOK = true
			case "text/event-stream":
				streamOK = true
			}
		}
	}
	return jsonOK && streamOK
}

func (s *routeStore) serveMCPBatch(w http.ResponseWriter, r *http.Request, host string, body []byte) {
	// 仅 2025-03-26 允许批处理，且初始化不能出现在批处理中。
	var requests []json.RawMessage
	if err := json.Unmarshal(body, &requests); err != nil {
		writeJSON(w, http.StatusBadRequest, mcpFailure(nil, -32700, "请求体不是有效的 UTF-8 JSON"))
		return
	}
	if len(requests) == 0 || len(requests) > mcpMaxBatch {
		writeJSON(w, http.StatusBadRequest, mcpFailure(nil, -32600, "批处理须包含 1–16 条消息"))
		return
	}
	replies := make([]mcpReply, 0, len(requests))
	status := http.StatusAccepted
	for _, request := range requests {
		reply, itemStatus := s.dispatchMCP(r, host, request, true)
		if reply.JSONRPC != "" {
			replies = append(replies, reply)
		} else if itemStatus != http.StatusAccepted {
			status = itemStatus
		}
	}
	if len(replies) == 0 {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, http.StatusOK, replies)
}

func (s *routeStore) dispatchMCP(r *http.Request, host string, raw []byte, batch bool) (mcpReply, int) {
	var request mcpObject
	if err := json.Unmarshal(raw, &request); err != nil || request == nil {
		if _, malformed := err.(*json.SyntaxError); malformed {
			return mcpFailure(nil, -32700, "请求体不是有效的 UTF-8 JSON"), http.StatusBadRequest
		}
		return mcpFailure(nil, -32600, "MCP 请求必须是 JSON 对象"), http.StatusBadRequest
	}
	if _, method := request["method"]; !method && (request["result"] != nil || request["error"] != nil) {
		// 此服务不向客户端发起请求，因此没有待处理的响应。
		return mcpReply{}, http.StatusBadRequest
	}
	id := request["id"]
	if len(id) != 0 && !mcpValidID(id) {
		return mcpFailure(nil, -32600, "请求 id 须为字符串或整数"), http.StatusBadRequest
	}
	method, methodOK := mcpString(request["method"])
	protocol, _ := mcpString(request["jsonrpc"])
	if protocol != "2.0" || !methodOK || method == "" || !request.only("jsonrpc", "id", "method", "params") {
		return mcpFailure(id, -32600, "无效的 JSON-RPC 2.0 请求"), http.StatusBadRequest
	}
	// 读完请求体再取策略；批处理中的每条消息也使用当时的入口与身份。
	c := s.current.Load()
	if status, _ := c.policy.checkRouteEntry(r, host, true); status != 0 {
		if len(id) == 0 {
			return mcpReply{}, status
		}
		return mcpFailure(id, -32000, "MCP 接口已不可用"), http.StatusOK
	}
	if _, _, ok := c.policy.authenticate(r); !ok {
		if len(id) == 0 {
			return mcpReply{}, http.StatusUnauthorized
		}
		return mcpFailure(id, -32000, authProblem(http.StatusUnauthorized).Error), http.StatusOK
	}
	params := request["params"]
	var fields mcpObject
	badParams := len(params) != 0 && (json.Unmarshal(params, &fields) != nil || fields == nil)
	if meta, exists := fields["_meta"]; exists && meta[0] != '{' {
		badParams = true
	}
	notification := strings.HasPrefix(method, "notifications/")
	if len(id) == 0 {
		// 无状态服务不记录初始化进度，也不执行没有 id 的请求方法。
		if badParams || !notification || method == "notifications/initialized" && !fields.only("_meta") {
			return mcpReply{}, http.StatusBadRequest
		}
		return mcpReply{}, http.StatusAccepted
	}
	if notification || batch && method == "initialize" {
		return mcpFailure(id, -32600, "此消息的请求形式不合法"), http.StatusBadRequest
	}
	if badParams {
		return mcpFailure(id, -32602, "params 和 _meta 须为 JSON 对象"), http.StatusOK
	}
	if r.Context().Err() != nil {
		return mcpFailure(id, -32000, "请求已超时或取消"), http.StatusOK
	}

	reply := mcpReply{JSONRPC: "2.0", ID: id}
	invalid := func(message string) (mcpReply, int) {
		return mcpFailure(id, -32602, message), http.StatusOK
	}
	switch method {
	case "initialize":
		requested, _ := mcpString(fields["protocolVersion"])
		var client mcpObject
		info := fields["clientInfo"]
		if len(info) != 0 {
			_ = json.Unmarshal(info, &client)
		}
		name, _ := mcpString(client["name"])
		clientVersion, _ := mcpString(client["version"])
		capabilities := fields["capabilities"]
		if !fields.only("protocolVersion", "capabilities", "clientInfo", "_meta") || requested == "" ||
			len(capabilities) == 0 || capabilities[0] != '{' || name == "" || clientVersion == "" {
			return invalid("初始化须提供 protocolVersion、capabilities 和 clientInfo（name、version）")
		}
		if !mcpSupportsVersion(requested) {
			requested = mcpLatest
		}
		reply.Result = map[string]any{
			"protocolVersion": requested,
			"capabilities":    map[string]any{"tools": struct{}{}},
			"serverInfo":      map[string]string{"name": "app-plug", "title": "应用插座 · App Plug", "version": Version},
		}
	case "ping":
		if !fields.only("_meta") {
			return invalid("ping 不接受参数")
		}
		reply.Result = struct{}{}
	case "tools/list":
		if _, hasCursor := fields["cursor"]; hasCursor {
			return invalid("工具列表只有一页，不接受 cursor")
		}
		if !fields.only("_meta") {
			return invalid("工具列表不接受其他参数")
		}
		reply.Result = struct {
			Tools []mcpTool `json:"tools"`
		}{mcpTools[:]}
	case "tools/call":
		name, ok := mcpString(fields["name"])
		var args mcpObject
		arguments := fields["arguments"]
		if !ok || name == "" || !fields.only("name", "arguments", "_meta") ||
			len(arguments) != 0 && (json.Unmarshal(arguments, &args) != nil || args == nil) {
			return invalid("工具调用须提供 name，arguments 须为 JSON 对象")
		}
		for i := range mcpTools {
			if mcpTools[i].Name == name {
				result, err := s.callMCPTool(r, c, host, &mcpTools[i], args)
				if err != nil {
					return mcpFailure(id, -32603, "无法编码工具响应"), http.StatusOK
				}
				reply.Result = result
				return reply, http.StatusOK
			}
		}
		return invalid("未知的工具名称")
	default:
		return mcpFailure(id, -32601, "未知的 MCP 方法"), http.StatusOK
	}
	return reply, http.StatusOK
}

func (m mcpObject) only(allowed ...string) bool {
	for name := range m {
		found := false
		for _, key := range allowed {
			if name == key {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func mcpValidID(id json.RawMessage) bool {
	// 保留原始 id，不强制转换成浮点数或机器位宽的整数。
	return id[0] == '"' || (id[0] == '-' || id[0] >= '0' && id[0] <= '9') && bytes.IndexAny(id, ".eE") == -1
}

func mcpString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var text string
	err := json.Unmarshal(raw, &text)
	return text, err == nil
}

type mcpTool struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	InputSchema mcpSchema `json:"inputSchema"`
}

type mcpSchema struct {
	Type                 string                 `json:"type"`
	AdditionalProperties bool                   `json:"additionalProperties"`
	Properties           map[string]mcpProperty `json:"properties,omitempty"`
	Required             []string               `json:"required,omitempty"`
	OneOf                []map[string]any       `json:"oneOf,omitempty"`
}

type mcpProperty struct {
	Type        string            `json:"type"`
	Description string            `json:"description"`
	MinLength   int               `json:"minLength,omitempty"`
	Minimum     int               `json:"minimum,omitempty"`
	Maximum     int               `json:"maximum,omitempty"`
	Items       map[string]string `json:"items,omitempty"`
}

var mcpEmptyInput = mcpSchema{Type: "object"}

var mcpDomain = mcpProperty{Type: "string", Description: "完整域名或单个域名前缀", MinLength: 1}

var mcpDomainInput = mcpSchema{
	Type: "object", Properties: map[string]mcpProperty{"domain": mcpDomain}, Required: []string{"domain"},
}

var mcpWriteInput = mcpSchema{
	Type: "object",
	Properties: map[string]mcpProperty{
		"domain":      mcpDomain,
		"name":        {Type: "string", Description: "应用显示名称；指定本机启动文件时可省略，默认使用域名", MinLength: 1},
		"description": {Type: "string", Description: "应用说明"},
		"address":     {Type: "string", Description: "受授权的私有或回环 IP 加端口", MinLength: 1},
		"startFile":   {Type: "string", Description: "本机可执行文件的绝对路径，仅管理员可配置", MinLength: 1},
		"args":        {Type: "array", Description: "传给本机可执行文件的参数，仅管理员可配置", Items: map[string]string{"type": "string"}},
		"autoStart":   {Type: "boolean", Description: "是否自动启动本机应用，省略时为 false"},
		"port":        {Type: "integer", Description: "本机应用监听的端口；省略时自动识别", Minimum: 1, Maximum: 65535},
	},
	Required: []string{"domain"},
	OneOf: []map[string]any{
		{"required": []string{"address", "name"}, "properties": map[string]bool{"startFile": false, "args": false, "autoStart": false, "port": false}},
		{"required": []string{"startFile"}, "properties": map[string]bool{"address": false}},
	},
}

var mcpTools = [...]mcpTool{
	{Name: "list_routes", Description: "列出当前令牌有权管理的路由。", InputSchema: mcpEmptyInput},
	{Name: "get_route", Description: "按域名读取一条有权管理的路由。", InputSchema: mcpDomainInput},
	{Name: "create_route", Description: "新建路由，域名已存在时拒绝。可指定上游地址；仅管理员可配置本机执行文件与参数。", InputSchema: mcpWriteInput},
	{Name: "update_route", Description: "替换同域名的路由，若不存在则新建。仅更新已有路由请使用 CLI app update 或 REST 条件更新（If-Match: *）。仅管理员可配置本机执行文件与参数。", InputSchema: mcpWriteInput},
	{Name: "delete_route", Description: "按域名删除一条有权管理的路由。", InputSchema: mcpDomainInput},
	{Name: "start_route", Description: "按域名启动本机托管应用。", InputSchema: mcpDomainInput},
	{Name: "stop_route", Description: "按域名停止本机托管应用。", InputSchema: mcpDomainInput},
}

type mcpToolResult struct {
	Content [1]mcpText `json:"content"`
	IsError bool       `json:"isError"`
}

type mcpText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func mcpToolText(text string, failed bool) mcpToolResult {
	return mcpToolResult{Content: [1]mcpText{{Type: "text", Text: text}}, IsError: failed}
}

func (s *routeStore) callMCPTool(r *http.Request, c *catalog, host string, tool *mcpTool, args mcpObject) (mcpToolResult, error) {
	input, problem := tool.InputSchema.parseArguments(args)
	if problem != "" {
		return mcpToolText(problem, true), nil
	}
	var status int
	var result any
	switch tool.Name {
	case "list_routes", "get_route":
		status, result = c.readRoutes(r, input.Domain)
	case "create_route", "update_route", "delete_route":
		method := http.MethodPost
		if tool.Name == "update_route" {
			method = http.MethodPut
		} else if tool.Name == "delete_route" {
			method = http.MethodDelete
		}
		status, result = s.applyRouteOperation(r, method, input.Domain, host, true, DeployInput{appInput: input})
	case "start_route", "stop_route":
		status, result = s.applyLifecycleOperation(r, input.Domain, host, true, tool.Name == "start_route")
	}
	if status < 200 || status >= 300 {
		if problem, ok := result.(apiProblem); ok {
			return mcpToolText(problem.Error, true), nil
		}
		return mcpToolText(http.StatusText(status), true), nil
	}
	if status == http.StatusNoContent && tool.Name == "delete_route" {
		return mcpToolText(`{"deleted":true}`, false), nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return mcpToolResult{}, err
	}
	return mcpToolText(string(data), false), nil
}

func (schema mcpSchema) parseArguments(args mcpObject) (appInput, string) {
	var input appInput
	for _, key := range schema.Required {
		if _, ok := args[key]; !ok {
			return input, "缺少参数 " + key
		}
	}
	for key, raw := range args {
		property, ok := schema.Properties[key]
		if !ok {
			return input, fmt.Sprintf("不支持参数 %q", key)
		}
		switch key {
		case "port":
			if json.Unmarshal(raw, &input.Port) != nil || input.Port < property.Minimum || input.Port > property.Maximum {
				return input, "port 须为 1–65535 的整数"
			}
			continue
		case "args":
			var values []json.RawMessage
			if json.Unmarshal(raw, &values) != nil || values == nil {
				return input, "args 须为字符串数组"
			}
			input.Args = make([]string, len(values))
			for i, raw := range values {
				value, ok := mcpString(raw)
				if !ok {
					return input, "args 须为字符串数组"
				}
				input.Args[i] = value
			}
			continue
		case "autoStart":
			switch string(raw) {
			case "true":
				input.AutoStart = true
			case "false":
				input.AutoStart = false
			default:
				return input, "autoStart 须为布尔值"
			}
			continue
		}
		value, ok := mcpString(raw)
		if !ok || property.MinLength > 0 && strings.TrimSpace(value) == "" {
			return input, fmt.Sprintf("参数 %s 须为有效字符串", key)
		}
		switch key {
		case "domain":
			input.Domain = value
		case "name":
			input.Name = value
		case "description":
			input.Description = value
		case "address":
			input.Address = value
		case "startFile":
			input.StartFile = value
		}
	}
	if len(schema.OneOf) != 0 {
		if (input.Address == "") == (input.StartFile == "") || input.StartFile == "" && (args["args"] != nil || args["autoStart"] != nil || args["port"] != nil) {
			return input, "请提供 address 或 startFile，args、autoStart、port 仅可与 startFile 一起使用"
		}
		if input.Address != "" && input.Name == "" {
			return input, "指定 address 时须提供 name"
		}
	}
	return input, ""
}
