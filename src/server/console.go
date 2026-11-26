package server

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed web/console/console.html
var consoleHTML string

//go:embed web/console/console.css
var consoleCSS string

//go:embed web/console/console.js
var consoleJS string

var consolePage = pageTemplate("console", consoleHTML)

func (p *policy) allowsConsole(host string) bool {
	return isIPHost(host) || p.APIMode == apiOn && host == p.HomeHost
}

const consoleIPRequired = "当前模式仅允许通过 IP 访问管理后台，请改用回环地址或 SSH 隧道"

func (s *routeStore) serveConsole(w http.ResponseWriter, r *http.Request, c *catalog, host, path string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if !c.policy.allowsConsole(host) {
		apiError(w, http.StatusForbidden, consoleIPRequired)
		return
	}
	if (path == "" || path == "/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodGet {
			view := struct{ AdminPath, APIPath, Version string }{c.policy.AdminPath, c.policy.APIPath, Version}
			if err := consolePage.Execute(w, view); err != nil {
				log.Printf("后台页面输出中断：%v", err)
			}
		}
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		var content, kind string
		switch path {
		case "/style.css":
			content, kind = consoleCSS, "text/css"
		case "/app.js":
			content, kind = consoleJS, "text/javascript"
		}
		if kind != "" {
			w.Header().Set("Content-Type", kind+"; charset=utf-8")
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, content)
			}
			return
		}
	}

	actor, denied := consoleActor(c.policy, r)
	if denied != 0 {
		writeConsoleResult(w, denied, consoleAuthProblem(denied))
		return
	}
	if path == "/data" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, c.consoleView(actor))
		return
	}
	change, ok := parseConsoleChange(path, r.Method)
	if !ok {
		apiError(w, http.StatusNotFound, "后台接口不存在")
		return
	}
	ctl := http.NewResponseController(w)
	if r.Method != http.MethodDelete {
		kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || kind != "application/json" {
			apiError(w, http.StatusUnsupportedMediaType, "请使用 application/json")
			return
		}
		_ = ctl.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := decodeJSON(r.Body, maxPolicyBytes, change.input); err != nil {
			if errors.Is(err, errJSONTooLarge) {
				apiError(w, http.StatusRequestEntityTooLarge, "请求体超出大小限制")
			} else {
				apiError(w, http.StatusBadRequest, "请求数据无效："+err.Error())
			}
			return
		}
	}
	_ = ctl.SetWriteDeadline(time.Now().Add(20 * time.Second))
	status, result := s.applyConsole(r, host, change)
	writeConsoleResult(w, status, result)
}

func consoleActor(p *policy, r *http.Request) (string, int) {
	actor, grant, ok := p.authenticate(r)
	if !ok {
		return "", http.StatusUnauthorized
	}
	if !grant.Admin {
		return "", http.StatusForbidden
	}
	return actor, 0
}

func consoleAuthProblem(status int) apiProblem {
	if status == http.StatusUnauthorized {
		return authProblem(status)
	}
	return apiProblem{"只有管理员可以使用后台"}
}

func writeConsoleResult(w http.ResponseWriter, status int, result any) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, result)
}

type consolePerson struct {
	Admin     bool                 `json:"admin"`
	Aliases   []string             `json:"aliases,omitempty"`
	Prefixes  []string             `json:"prefixes,omitempty"`
	Nodes     map[string]portRange `json:"nodes,omitempty"`
	Domains   []string             `json:"domains,omitempty"`
	Networks  []string             `json:"networks,omitempty"`
	Launchers []string             `json:"launchers,omitempty"`
}

type consoleData struct {
	Actor      string                   `json:"actor"`
	BaseDomain string                   `json:"baseDomain"`
	HomeHost   string                   `json:"homeHost"`
	ListenPort int                      `json:"listenPort"`
	APIMode    apiMode                  `json:"apiMode"`
	MCPPath    string                   `json:"mcpPath"`
	Routes     []consoleApp             `json:"routes"`
	Nodes      map[string]node          `json:"nodes"`
	Principals map[string]consolePerson `json:"principals"`
}

type consoleApp struct {
	Domain      string   `json:"domain"`
	Address     string   `json:"address"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url"`
	Managed     bool     `json:"managed,omitempty"`
	StartFile   string   `json:"startFile,omitempty"`
	Args        []string `json:"args,omitempty"`
	AutoStart   bool     `json:"autoStart"`
	FileManaged bool     `json:"fileManaged,omitempty"`
	Running     bool     `json:"running,omitempty"`
	Ready       bool     `json:"ready,omitempty"`
	Upstream    string   `json:"upstream,omitempty"`
	Port        int      `json:"port,omitempty"`
}

func (c *catalog) consoleView(actor string) consoleData {
	p := c.policy
	nodes := p.Nodes
	if nodes == nil {
		nodes = map[string]node{}
	}
	v := consoleData{Actor: actor, BaseDomain: p.BaseDomain, HomeHost: p.HomeHost, ListenPort: int(p.listener.Port()), APIMode: p.APIMode, MCPPath: p.MCPPath,
		Routes: make([]consoleApp, 0, len(c.apps)), Nodes: nodes, Principals: make(map[string]consolePerson, len(p.Principals))}
	for _, a := range c.apps {
		row := consoleApp{Domain: a.host, Address: a.target.Host, Name: a.Name, Description: a.Description, URL: "https://" + a.host + "/"}
		if a.local != nil {
			row.Managed, row.FileManaged = true, a.fileManaged()
			row.StartFile, row.Args, row.Port = a.local.StartFile, a.local.Args, int(a.local.Port)
			row.AutoStart, row.Running = a.local.startsAutomatically(), a.process.running()
			if target := a.upstream(); target != nil {
				row.Ready, row.Upstream = true, target.Host
			}
		}
		v.Routes = append(v.Routes, row)
	}
	for name, grant := range p.Principals {
		v.Principals[name] = consolePerson{grant.Admin, grant.Aliases, grant.Prefixes, grant.Nodes, grant.Domains, grant.Networks, grant.Launchers}
	}
	return v
}

type consoleChange struct {
	area, name string
	input      any
}

type consoleNodeInput struct {
	node
	Create *bool `json:"create"`
}
type consolePersonInput struct {
	consolePerson
	Create *bool `json:"create"`
}
type consoleSettings struct {
	APIMode apiMode `json:"apiMode"`
}

type consoleMCPSettings struct {
	MCPPath string `json:"mcpPath"`
}
type consoleTokenInput struct {
	Token string `json:"token"`
}

var validManualToken = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{16,128}$`)

type issuedToken struct {
	Token string `json:"token"`
}

func parseConsoleChange(path, method string) (consoleChange, bool) {
	if path == "/settings" && method == http.MethodPut {
		return consoleChange{area: "settings", input: new(consoleSettings)}, true
	}
	if path == "/settings/mcp" && method == http.MethodPut {
		return consoleChange{area: "mcp", input: new(consoleMCPSettings)}, true
	}
	if path == "/deploy-keys" && method == http.MethodPost {
		return consoleChange{area: "deploy-keys", input: new(appInput)}, true
	}
	if path == "/routes" && method == http.MethodPost {
		return consoleChange{area: "routes", input: new(appInput)}, true
	}
	area, name, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if !ok {
		return consoleChange{}, false
	}
	if area == "routes" && method == http.MethodPost {
		domain, action, ok := strings.Cut(name, "/")
		if ok && dnsName(domain) && !isIPHost(domain) && (action == "start" || action == "stop") {
			return consoleChange{area: action, name: domain, input: new(struct{})}, true
		}
	}
	if area == "routes" && dnsName(name) && !isIPHost(name) && (method == http.MethodPut || method == http.MethodDelete) {
		return consoleChange{area: area, name: name, input: new(appInput)}, true
	}
	if area == "principals" && method == http.MethodPost {
		name, ok = strings.CutSuffix(name, "/rotate")
		if ok && validAlias.MatchString(name) {
			return consoleChange{area: "rotate", name: name, input: new(consoleTokenInput)}, true
		}
		return consoleChange{}, false
	}
	if !validAlias.MatchString(name) {
		return consoleChange{}, false
	}
	change := consoleChange{area: area, name: name}
	switch area {
	case "nodes":
		change.input = new(consoleNodeInput)
	case "principals":
		change.input = new(consolePersonInput)
	default:
		return consoleChange{}, false
	}
	return change, method == http.MethodPut || method == http.MethodDelete
}

func (s *routeStore) applyConsole(r *http.Request, host string, change consoleChange) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.current.Load()
	if !hasPathPrefix(r.URL.EscapedPath(), c.policy.AdminPath) {
		return http.StatusNotFound, apiProblem{"后台接口不存在"}
	}
	if !c.policy.allowsConsole(host) {
		return http.StatusForbidden, apiProblem{consoleIPRequired}
	}
	actor, denied := consoleActor(c.policy, r)
	if denied != 0 {
		return denied, consoleAuthProblem(denied)
	}
	if change.area == "deploy-keys" {
		return s.issueDeployKey(c, actor, bearerToken(r), *change.input.(*appInput))
	}
	name := change.name
	if change.area == "routes" {
		return s.applyConsoleRoute(c, actor, r.Method, name, change.input.(*appInput))
	}
	if change.area == "start" || change.area == "stop" {
		return s.controlLocal(c, actor, c.byHost[name], change.area == "start")
	}

	next := *c.policy
	status := http.StatusNoContent
	var response any
	switch change.area {
	case "settings":
		mode := change.input.(*consoleSettings).APIMode
		if mode == "" {
			return http.StatusBadRequest, apiProblem{"请选择管理 API 模式"}
		}
		next.APIMode = mode
	case "mcp":
		next.MCPPath = change.input.(*consoleMCPSettings).MCPPath
	case "nodes":
		_, exists := next.Nodes[name]
		next.Nodes = maps.Clone(next.Nodes)
		if r.Method == http.MethodDelete {
			if !exists {
				return http.StatusNotFound, apiProblem{"节点不存在"}
			}
			delete(next.Nodes, name)
		} else {
			x := change.input.(*consoleNodeInput)
			if stat, msg := expectResource(x.Create, exists); stat != 0 {
				return stat, apiProblem{msg}
			}
			if next.Nodes == nil {
				next.Nodes = make(map[string]node)
			}
			next.Nodes[name] = x.node
		}
	case "principals", "rotate":
		old, exists := next.Principals[name]
		next.Principals = maps.Clone(next.Principals)
		if r.Method == http.MethodDelete {
			if !exists {
				return http.StatusNotFound, apiProblem{"身份不存在"}
			}
			if name == actor {
				return http.StatusConflict, apiProblem{"不能删除正在登录的管理员"}
			}
			for _, a := range c.apps {
				if a.Owner == name {
					return http.StatusConflict, apiProblem{"该身份仍拥有应用路由，请先移除对应路由"}
				}
			}
			delete(next.Principals, name)
			break
		}
		if change.area == "principals" {
			x := change.input.(*consolePersonInput)
			if stat, msg := expectResource(x.Create, exists); stat != 0 {
				return stat, apiProblem{msg}
			}
			if name == actor && !x.Admin {
				return http.StatusConflict, apiProblem{"不能移除当前身份的管理员权限"}
			}
			if !exists {
				for _, a := range c.apps {
					if a.Owner == name {
						return http.StatusConflict, apiProblem{"该名称仍关联历史路由，请先移除对应路由"}
					}
				}
			}
			old.Admin, old.Aliases, old.Prefixes, old.Nodes = x.Admin, x.Aliases, x.Prefixes, x.Nodes
			old.Domains, old.Networks = x.Domains, x.Networks
			if old.Admin {
				old.Launchers = nil
			} else if x.Launchers != nil {
				old.Launchers = x.Launchers
			}
		} else if !exists {
			return http.StatusNotFound, apiProblem{"身份不存在"}
		}
		if change.area == "rotate" {
			token := change.input.(*consoleTokenInput).Token
			if !validManualToken.MatchString(token) {
				return http.StatusBadRequest, apiProblem{"新令牌须为 16–128 个字符，仅允许英文字母、数字及 . _ ~ + / = -"}
			}
			digest := sha256.Sum256([]byte(token))
			nextDigest := hex.EncodeToString(digest[:])
			if strings.EqualFold(old.TokenSHA256, nextDigest) {
				return http.StatusBadRequest, apiProblem{"新令牌不能与当前令牌相同"}
			}
			old.TokenSHA256 = nextDigest
		} else if !exists {
			token, digest, err := newAdminToken()
			if err != nil {
				log.Printf("无法生成后台令牌：%v", err)
				return http.StatusInternalServerError, apiProblem{"令牌生成失败"}
			}
			old.TokenSHA256 = digest
			response = issuedToken{token}
			status = http.StatusCreated
		}
		next.Principals[name] = old
	}
	updated, err := s.catalogForPolicy(c, &next)
	if err != nil {
		return http.StatusBadRequest, apiProblem{err.Error()}
	}
	// 文件由后台管理；若操作者已另存未重载的修改，不覆盖其内容。
	file, err := filepath.EvalSymlinks(s.configPath)
	if err != nil {
		return http.StatusConflict, apiProblem{"配置文件不可用，请核对部署配置"}
	}
	disk, err := loadPolicy(file, s.listenOverride)
	if err != nil || !sameStoredPolicy(c.policy, disk) {
		return http.StatusConflict, apiProblem{"磁盘配置与运行配置不一致，请先校验并重载"}
	}
	if err := writeJSONAtomic(file, &next, maxPolicyBytes); err != nil {
		log.Printf("持久化后台配置失败：%v", err)
		return http.StatusInternalServerError, apiProblem{"配置写入失败，原配置未改变"}
	}
	s.publishPolicy(updated)
	log.Printf("身份 %s：%s 后台 %s %q", actor, r.Method, change.area, name)
	return status, response
}

func (s *routeStore) applyConsoleRoute(c *catalog, actor, method, domain string, input *appInput) (int, any) {
	old := c.byHost[domain]
	if old != nil && old.fileManaged() {
		return http.StatusForbidden, apiProblem{"localApps 声明的应用只能在本机配置文件中修改"}
	}
	if method != http.MethodPost && old == nil {
		return http.StatusNotFound, apiProblem{"应用已不存在，请刷新"}
	}
	var next *app
	if method != http.MethodDelete {
		var err error
		next, err = c.policy.resolveApp(*input, actor)
		if err != nil {
			return http.StatusBadRequest, apiProblem{err.Error()}
		}
		// 只改文案时保留原有路由和部署授权，包括旧节点和启动项。
		if old != nil && next.host == old.host && next.target == old.target &&
			(next.local == nil && old.local == nil || next.local != nil && old.local != nil &&
				next.local.sameCommand(old.local) && next.local.startsAutomatically() == old.local.startsAutomatically()) {
			name, description := next.Name, next.Description
			next.route = old.route
			next.Name, next.Description = name, description
			next.status.Store(old.status.Load())
		}
	}
	status, result := s.persistRoute(c, actor, method, old, next)
	if status == http.StatusOK || status == http.StatusCreated {
		return http.StatusNoContent, nil
	}
	return status, result
}

func sameStoredPolicy(a, b *policy) bool {
	// 比较序列化后的策略，忽略 omitempty 产生的 nil 和空集合差异。
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && string(left) == string(right)
}

func expectResource(create *bool, exists bool) (int, string) {
	if create == nil {
		return http.StatusBadRequest, "必须明确是新建还是更新"
	}
	if *create && exists {
		return http.StatusConflict, "名称已存在，请刷新后选择编辑"
	}
	if !*create && !exists {
		return http.StatusNotFound, "记录已不存在，请刷新"
	}
	return 0, ""
}

func (s *routeStore) catalogForPolicy(c *catalog, next *policy) (*catalog, error) {
	if err := next.validate(s.listenOverride); err != nil {
		return nil, err
	}
	updated, err := next.catalog(c.storedRoutes())
	if err != nil {
		return nil, err
	}
	for i, a := range updated.apps {
		if old := c.byHost[a.host]; old != nil && a.local == nil && old.local == nil && a.target == old.target {
			updated.apps[i], updated.byHost[a.host] = old, old
		}
	}
	return updated, nil
}
