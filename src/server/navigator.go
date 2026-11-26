package server

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed web/home/home.html
var homeHTML string

//go:embed web/home/home.css
var homeCSS string

//go:embed web/home/copy.js
var copyJS string

//go:embed web/shared/appearance.html
var appearanceHTML string

//go:embed web/shared/theme.css
var themeCSS string

//go:embed web/shared/theme.js
var themeJS string

//go:embed web/shared/app-plug-mark.svg
var brandMark string

//go:embed web/shared/app-plug-lockup.svg
var brandLockup string

//go:embed web/shared/app-plug-lockup-dark.svg
var brandLockupDark string

var home = pageTemplate("home", homeHTML)

func pageTemplate(name, html string) *template.Template {
	t := template.Must(template.New(name).Parse(appearanceHTML))
	return template.Must(t.Parse(html))
}

type navigator struct {
	store     *routeStore
	transport *http.Transport
	proxy     *httputil.ReverseProxy
}

func newNavigator(store *routeStore) *navigator {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       45 * time.Second,
		MaxIdleConns:          maxApps,
		DisableCompression:    true,
	}
	n := &navigator{store: store, transport: transport}
	n.proxy = &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			// 请求开始时绑定目标；后续路由变更不影响正在转发的请求。
			target := r.In.Context().Value(upstreamKey{}).(*url.URL)
			r.Out.URL.Scheme, r.Out.URL.Host = target.Scheme, target.Host
			// 保留原始路径和查询；入口只对外提供 HTTPS。
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Host = r.In.Host
			r.Out.Header.Del("X-Forwarded-Prefix")
			r.Out.Header.Del("X-Real-IP")
			r.Out.Header.Del("X-Forwarded-Port")
			r.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				http.Error(w, "目标应用响应超时", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, "无法连接目标应用", http.StatusBadGateway)
		},
	}
	return n
}

func (n *navigator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.IsAbs() || !strings.HasPrefix(r.RequestURI, "/") || strings.HasPrefix(r.RequestURI, "//") {
		http.Error(w, "仅接受站内路径", http.StatusBadRequest)
		return
	}
	c := n.store.current.Load()
	host, ok := requestHost(r.Host)
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := r.URL.EscapedPath()
	if host == c.policy.HomeHost || isIPHost(host) {
		switch {
		case path == c.policy.MCPPath:
			n.store.serveMCP(w, r, c, host)
			return
		case hasPathPrefix(path, c.policy.AdminPath):
			n.store.serveConsole(w, r, c, host, path[len(c.policy.AdminPath):])
			return
		case hasPathPrefix(path, c.policy.APIPath):
			n.store.serveAdmin(w, r, c, host)
			return
		case path == "/_app-plug/theme.css":
			serveAsset(w, r, "text/css", themeCSS)
			return
		case path == "/_app-plug/theme.js":
			serveAsset(w, r, "text/javascript", themeJS)
			return
		case path == "/_app-plug/app-plug-mark.svg":
			serveAsset(w, r, "image/svg+xml", brandMark)
			return
		case path == "/_app-plug/app-plug-lockup.svg":
			serveAsset(w, r, "image/svg+xml", brandLockup)
			return
		case path == "/_app-plug/app-plug-lockup-dark.svg":
			serveAsset(w, r, "image/svg+xml", brandLockupDark)
			return
		}
	}
	if host == c.policy.HomeHost {
		serveHome(w, r, c)
		return
	}
	a := c.byHost[host]
	if a == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodConnect || r.Method == http.MethodTrace {
		http.Error(w, "不支持此请求方法", http.StatusMethodNotAllowed)
		return
	}
	target := a.upstream()
	if target == nil {
		http.Error(w, "托管应用尚无可用监听", http.StatusServiceUnavailable)
		return
	}
	n.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), upstreamKey{}, target)))
}

type upstreamKey struct{}

func requestHost(authority string) (string, bool) {
	host := authority
	if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
		host = authority[1 : len(authority)-1]
	} else if strings.Contains(authority, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return "", false
		}
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return "", false
		}
	}
	if strings.HasPrefix(authority, "[") {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", false
		}
	}
	return strings.ToLower(host), true
}

func isIPHost(host string) bool {
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Zone() == ""
}

func serveHome(w http.ResponseWriter, r *http.Request, c *catalog) {
	switch r.URL.EscapedPath() {
	case "/_app-plug/style.css":
		serveAsset(w, r, "text/css", homeCSS)
	case "/_app-plug/copy.js":
		serveAsset(w, r, "text/javascript", copyJS)
	case "/":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "此路径只读", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		renderHome(w, r, c)
	default:
		http.NotFound(w, r)
	}
}

func serveAsset(w http.ResponseWriter, r *http.Request, contentType, body string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "此路径只读", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, body)
	}
}

type appView struct {
	Name, Description, Host, StateClass, State, Detail string
	HTTPStatus                                         int32
}

type homeView struct {
	Apps       []appView
	Reachable  int
	BaseDomain string
	APIPath    string
	Version    string
}

func renderHome(w http.ResponseWriter, r *http.Request, c *catalog) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if r.Method == http.MethodHead {
		return
	}
	views := make([]appView, 0, len(c.apps))
	reachable := 0
	for _, a := range c.apps {
		v := appView{Name: a.Name, Description: a.Description, Host: a.host}
		if a.local != nil && a.upstream() == nil {
			v.StateClass, v.State, v.Detail = "down", "未就绪", "托管应用尚无可用监听"
			views = append(views, v)
			continue
		}
		switch status := a.status.Load(); status {
		case 0:
			v.StateClass, v.State, v.Detail = "pending", "未检查", "等待首次检查"
		case probeTimeoutStatus:
			v.StateClass, v.State, v.Detail = "down", "检查超时", "2 秒内未收到响应"
		case probeErrorStatus:
			v.StateClass, v.State, v.Detail = "down", "不可达", "连接失败或无有效 HTTP 响应"
		default:
			v.StateClass, v.State, v.HTTPStatus = "up", "可达", status
			reachable++
		}
		views = append(views, v)
	}
	if err := home.Execute(w, homeView{Apps: views, Reachable: reachable, BaseDomain: c.policy.BaseDomain, APIPath: c.policy.APIPath, Version: Version}); err != nil {
		log.Printf("导航页输出中断：%v", err)
	}
}
