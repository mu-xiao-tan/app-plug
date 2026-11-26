package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// RouteEndpoint 是相对于已配置 API 前缀的应用集合路径。
const RouteEndpoint = "/routes"

// MaxDeployResponseBytes 限制 CLI 在一次部署请求中读取的数据量。
const MaxDeployResponseBytes = maxStateBytes + maxRouteBytes

// DeployInput 是部署接口新增或更新应用时使用的 JSON。
type DeployInput struct {
	appInput
	Node      string `json:"node,omitempty"`
	NewDomain string `json:"newDomain,omitempty"`
}

func (s *routeStore) serveAdmin(w http.ResponseWriter, r *http.Request, c *catalog, host string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if status, problem := c.policy.checkAPIHost(host); status != 0 {
		writeAdminResult(w, status, problem)
		return
	}
	// 与应用反代共用监听，但管理请求仍需限制请求体和响应的等待时间。
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
	_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
	path := strings.TrimPrefix(r.URL.EscapedPath(), c.policy.APIPath)
	key, member := strings.CutPrefix(path, RouteEndpoint+"/")
	if domain, action, ok := strings.Cut(key, "/"); member && ok && r.Method == http.MethodPost && (action == "start" || action == "stop") {
		status, result := s.applyLifecycleOperation(r, domain, host, false, action == "start")
		writeAdminResult(w, status, result)
		return
	}
	collection := path == RouteEndpoint
	if collection {
		key = ""
	}
	if !collection && (!member || key == "") {
		apiError(w, http.StatusNotFound, "接口或域名不存在")
		return
	}
	if r.Method == http.MethodGet {
		status, result := c.readRoutes(r, key)
		writeAdminResult(w, status, result)
		return
	}
	if !(collection && r.Method == http.MethodPost || member && (r.Method == http.MethodPut || r.Method == http.MethodDelete)) {
		if collection {
			w.Header().Set("Allow", "GET, POST")
		} else {
			w.Header().Set("Allow", "GET, PUT, DELETE")
		}
		apiError(w, http.StatusMethodNotAllowed, "不支持此请求方法")
		return
	}
	var input DeployInput
	var claim deployKeyClaim
	temporary := false
	// 不在写锁内读取网络请求体。
	if r.Method != http.MethodDelete {
		_, _, authenticated := c.policy.authenticate(r)
		temporary = !authenticated && collection && r.Method == http.MethodPost && strings.HasPrefix(bearerToken(r), deployKeyPrefix)
		if !authenticated && !temporary {
			writeAdminResult(w, http.StatusUnauthorized, authProblem(http.StatusUnauthorized))
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			apiError(w, http.StatusUnsupportedMediaType, "请使用 application/json")
			return
		}
		var body any = &input
		if temporary {
			body = &claim
		}
		if err := decodeJSON(r.Body, maxRouteBytes, body); err != nil {
			if errors.Is(err, errJSONTooLarge) {
				apiError(w, http.StatusRequestEntityTooLarge, "请求体不得超过 8 KiB")
			} else if temporary {
				apiError(w, http.StatusBadRequest, "使用临时密钥时，请求体只需填写已授权的 domain")
			} else {
				apiError(w, http.StatusBadRequest, "请求体字段无效；请填写 address 或 startFile")
			}
			return
		}
	}
	if temporary {
		status, result := s.redeemDeployKey(r, host, c.policy.APIPath, claim)
		writeAdminResult(w, status, result)
		return
	}
	status, result := s.applyRouteOperation(r, r.Method, key, host, false, input)
	writeAdminResult(w, status, result)
}

func (c *catalog) readRoutes(r *http.Request, key string) (int, any) {
	if key != "" {
		_, _, a, status := c.authorize(r, key)
		if status != 0 {
			return status, authProblem(status)
		}
		if a == nil {
			return http.StatusNotFound, authProblem(http.StatusNotFound)
		}
		return http.StatusOK, a.response()
	}
	actor, grant, ok := c.policy.authenticate(r)
	if !ok {
		return http.StatusUnauthorized, authProblem(http.StatusUnauthorized)
	}
	result := make([]RouteResponse, 0, len(c.apps))
	for _, a := range c.apps {
		if c.canManageRoute(actor, grant, a.host, a) {
			result = append(result, a.response())
		}
	}
	return http.StatusOK, result
}

func (c *catalog) authorize(r *http.Request, key string) (string, principal, *app, int) {
	owner, grant, ok := c.policy.authenticate(r)
	if !ok {
		return "", principal{}, nil, http.StatusUnauthorized
	}
	domain, err := c.policy.appDomain(key)
	if err != nil {
		return "", principal{}, nil, http.StatusNotFound
	}
	old := c.byHost[domain]
	if !c.canManageRoute(owner, grant, domain, old) {
		return "", principal{}, nil, http.StatusForbidden
	}
	return owner, grant, old, 0
}

func (c *catalog) canManageRoute(actor string, grant principal, domain string, a *app) bool {
	if !grant.allowsDomain(domain, c.policy.BaseDomain) {
		return false
	}
	if a == nil || grant.Admin {
		return true
	}
	return a.Owner == actor && (a.local == nil || a.Launcher != "" && grant.allowsLauncher(a.Launcher))
}

func bearerToken(r *http.Request) string {
	headers := r.Header.Values("Authorization")
	if len(headers) == 1 {
		scheme, token, ok := strings.Cut(headers[0], " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			return token
		}
	}
	return ""
}

func (p *policy) authenticate(r *http.Request) (string, principal, bool) {
	if token := bearerToken(r); token != "" {
		if owner, ok := p.byToken[sha256.Sum256([]byte(token))]; ok {
			return owner, p.Principals[owner], true
		}
	}
	return "", principal{}, false
}

func authProblem(status int) apiProblem {
	if status == http.StatusUnauthorized {
		return apiProblem{"需要有效的 Bearer 令牌"}
	}
	if status == http.StatusNotFound {
		return apiProblem{"域名或路由不存在"}
	}
	return apiProblem{"没有管理此域名或路由的权限"}
}

func writeAdminResult(w http.ResponseWriter, status int, result any) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="app-plug"`)
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, result)
}

func (s *routeStore) applyRouteOperation(r *http.Request, method, key, host string, viaMCP bool, input DeployInput) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Context().Err() != nil {
		return http.StatusRequestTimeout, apiProblem{"请求已超时或取消"}
	}
	c := s.current.Load()
	if status, problem := c.policy.checkRouteEntry(r, host, viaMCP); status != 0 {
		return status, problem
	}
	if method == http.MethodPost {
		if input.Domain == "" || input.Node != "" || input.NewDomain != "" {
			return http.StatusBadRequest, apiProblem{"请填写域名与上游地址或本机启动文件"}
		}
		key = input.Domain
	}
	actor, grant, old, denied := c.authorize(r, key)
	if denied != 0 {
		return denied, authProblem(denied)
	}
	if method == http.MethodPost && old != nil {
		return http.StatusConflict, apiProblem{"域名已被使用"}
	}
	if method == http.MethodPut && old == nil && (input.NewDomain != "" || r.Header.Get("If-Match") == "*") {
		return http.StatusPreconditionFailed, apiProblem{"待更新的路由已不存在"}
	}
	if old != nil && old.fileManaged() {
		return http.StatusForbidden, apiProblem{"localApps 声明的应用只能在配置文件中编辑或移除"}
	}
	if method == http.MethodDelete {
		return s.persistRoute(c, actor, method, old, nil)
	}
	owner := actor
	if old != nil {
		owner = old.Owner
	}
	if input.StartFile != "" && !grant.Admin {
		return http.StatusForbidden, apiProblem{"只有管理员能设置本机启动文件和参数"}
	}
	if method == http.MethodPut && input.NewDomain == "" && input.StartFile == "" && input.Launcher == "" && input.Address == "" && input.Domain == "" &&
		len(input.Args) == 0 && !input.AutoStart && validAlias.MatchString(key) && (input.Node != "" || input.Port != 0) {
		legacy := routeInput{Node: input.Node, Port: input.Port, Name: input.Name, Description: input.Description}
		if !grant.allowsTarget(legacy) {
			return http.StatusForbidden, apiProblem{"目标节点或端口未授权"}
		}
		next, err := c.policy.resolve(route{Alias: key, Owner: owner, routeInput: legacy})
		if err != nil {
			return http.StatusBadRequest, apiProblem{err.Error()}
		}
		return s.persistRoute(c, actor, method, old, next)
	}
	if input.Node != "" {
		return http.StatusBadRequest, apiProblem{"不能混用旧节点配置"}
	}
	domain, _ := c.policy.appDomain(key)
	if input.Domain == "" {
		input.Domain = domain
	}
	if requested, err := c.policy.appDomain(input.Domain); err != nil || requested != domain {
		return http.StatusBadRequest, apiProblem{"请求体域名必须与接口域名一致"}
	}
	if input.NewDomain != "" {
		target, err := c.policy.appDomain(input.NewDomain)
		if err != nil {
			return http.StatusBadRequest, apiProblem{err.Error()}
		}
		if !grant.allowsDomain(target, c.policy.BaseDomain) {
			return http.StatusForbidden, apiProblem{"新域名未获授权"}
		}
		input.Domain = target
	}
	next, err := c.policy.resolveApp(input.appInput, owner)
	if err != nil {
		return http.StatusBadRequest, apiProblem{err.Error()}
	}
	if next.local == nil {
		addr, _ := netip.ParseAddrPort(next.Address)
		if !c.policy.allowsAddress(grant, addr) {
			return http.StatusForbidden, apiProblem{"目标 IP 或端口未授权"}
		}
	}
	return s.persistRoute(c, actor, method, old, next)
}

func (s *routeStore) applyLifecycleOperation(r *http.Request, domain, host string, viaMCP, start bool) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Context().Err() != nil {
		return http.StatusRequestTimeout, apiProblem{"请求已超时或取消"}
	}
	c := s.current.Load()
	if status, problem := c.policy.checkRouteEntry(r, host, viaMCP); status != 0 {
		return status, problem
	}
	actor, _, a, denied := c.authorize(r, domain)
	if denied != 0 {
		return denied, authProblem(denied)
	}
	return s.controlLocal(c, actor, a, start)
}

// 启停只改变本次导航进程的运行状态，不写入路由配置。
func (s *routeStore) controlLocal(c *catalog, actor string, a *app, start bool) (int, any) {
	if a == nil {
		return http.StatusNotFound, apiProblem{"应用不存在"}
	}
	if a.local == nil {
		return http.StatusConflict, apiProblem{"已有服务不由导航启停"}
	}
	if s.locals.closed {
		return http.StatusServiceUnavailable, apiProblem{"导航正在停止"}
	}
	entry := s.locals.apps[a.host]
	if start {
		if entry.process.running() {
			return http.StatusOK, a.response()
		}
		p, err := startLocal(a.host, *a.local, c.policy.listener.Port(), s.changed)
		if err != nil {
			return http.StatusConflict, apiProblem{"应用启动失败：" + err.Error()}
		}
		entry.process = p
	} else {
		if entry.process == nil {
			return http.StatusOK, a.response()
		}
		s.locals.stop(a.host)
		entry.process = nil
	}
	s.locals.apps[a.host] = entry
	apps := slices.Clone(c.apps)
	for i, old := range apps {
		if old == a {
			apps[i] = &app{route: a.route, host: a.host, local: a.local, process: entry.process}
			break
		}
	}
	s.publish(newCatalog(c.policy, apps))
	log.Printf("身份 %s：调整托管应用 %s，启动=%t", actor, a.host, start)
	return http.StatusOK, s.current.Load().byHost[a.host].response()
}

// 调用者持有写锁并已完成鉴权。
func (s *routeStore) persistRoute(c *catalog, actor, method string, old, next *app) (int, any) {
	var domain string
	status := http.StatusNoContent
	if next != nil {
		domain = next.host
		if other := c.byHost[domain]; other != nil && other != old {
			return http.StatusConflict, apiProblem{"域名已被使用"}
		}
		status = http.StatusOK
		if old != nil && sameRoute(old.route, next.route) {
			return status, old.response()
		}
		if old == nil {
			if len(c.apps) == maxApps {
				return http.StatusConflict, apiProblem{"路由数量已达上限"}
			}
			status = http.StatusCreated
		}
	} else {
		if old == nil {
			return http.StatusNotFound, apiProblem{"路由不存在"}
		}
		domain = old.host
	}
	if err := s.saveRoute(c, old, next); err != nil {
		log.Printf("持久化路由 %q 失败：%v", domain, err)
		return http.StatusInternalServerError, apiProblem{"路由保存失败，原路由未改变"}
	}
	log.Printf("身份 %s：%s 路由 %s", actor, method, domain)
	if next == nil {
		return status, nil
	}
	return status, s.current.Load().byHost[domain].response()
}

func (s *routeStore) saveRoute(c *catalog, old, next *app) error {
	apps := make([]*app, 0, len(c.apps)+1)
	for _, a := range c.apps {
		if a != old {
			apps = append(apps, a)
		}
	}
	if next != nil {
		apps = append(apps, next)
	}
	return s.save(newCatalog(c.policy, apps))
}

// 等待写锁期间可能重载策略，按实际入口重新校验，而不是用 REST 开关控制 MCP。
func (p *policy) checkRouteEntry(r *http.Request, host string, viaMCP bool) (int, apiProblem) {
	path := r.URL.EscapedPath()
	if viaMCP {
		if path != p.MCPPath {
			return http.StatusNotFound, apiProblem{"接口不存在"}
		}
		return p.checkMCPHost(host)
	}
	if !hasPathPrefix(path, p.APIPath) {
		return http.StatusNotFound, apiProblem{"接口不存在"}
	}
	return p.checkAPIHost(host)
}

func (p *policy) checkMCPHost(host string) (int, apiProblem) {
	if p.MCPMode == apiOff {
		return http.StatusNotFound, apiProblem{"接口不存在"}
	}
	if isIPHost(host) || p.MCPMode == apiOn && host == p.HomeHost {
		return 0, apiProblem{}
	}
	return http.StatusForbidden, apiProblem{"当前 MCP 策略不允许此访问地址"}
}

func (p *policy) checkAPIHost(host string) (int, apiProblem) {
	if p.APIMode == apiOff {
		return http.StatusNotFound, apiProblem{"接口不存在"}
	}
	if isIPHost(host) || p.APIMode == apiOn && host == p.HomeHost {
		return 0, apiProblem{}
	}
	return http.StatusForbidden, apiProblem{"当前管理 API 策略不允许此访问地址"}
}

// RouteResponse 是部署接口返回的路由及可选的本机运行状态。
type RouteResponse struct {
	route
	URL         string `json:"url"`
	Upstream    string `json:"upstream,omitempty"`
	Ready       *bool  `json:"ready,omitempty"`
	Running     *bool  `json:"running,omitempty"`
	AutoStart   *bool  `json:"autoStart,omitempty"`
	FileManaged bool   `json:"fileManaged,omitempty"`
}

func (a *app) response() RouteResponse {
	result := RouteResponse{route: a.route, URL: "https://" + a.host + "/"}
	if a.local != nil {
		result.FileManaged = a.fileManaged()
		result.Domain = a.host
		result.Launcher = ""
		result.Port = int(a.local.Port)
		result.StartFile = a.local.StartFile
		result.Args = a.local.Args
		autoStart := a.local.startsAutomatically()
		result.AutoStart = &autoStart
		running := a.process.running()
		result.Running = &running
		target := a.upstream()
		ready := target != nil
		result.Ready = &ready
		if ready {
			result.Upstream = target.Host
		}
	}
	return result
}

type apiProblem struct {
	Error string `json:"error"`
}

func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiProblem{message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("管理 API 响应中断：%v", err)
	}
}
