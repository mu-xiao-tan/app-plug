package server

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

const maxApps = 64

type route struct {
	Alias     string   `json:"alias,omitempty"`
	Owner     string   `json:"owner"`
	Domain    string   `json:"domain,omitempty"`
	Address   string   `json:"address,omitempty"`
	Launcher  string   `json:"launcher,omitempty"`
	StartFile string   `json:"startFile,omitempty"`
	Args      []string `json:"args,omitempty"`
	AutoStart bool     `json:"autoStart,omitempty"`
	routeInput
}

type routeInput struct {
	Node        string `json:"node,omitempty"`
	Port        int    `json:"port,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type appInput struct {
	Domain      string   `json:"domain"`
	Address     string   `json:"address"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Launcher    string   `json:"launcher,omitempty"`
	Port        int      `json:"port,omitempty"`
	StartFile   string   `json:"startFile,omitempty"`
	Args        []string `json:"args,omitempty"`
	AutoStart   bool     `json:"autoStart,omitempty"`
}

func (p *policy) resolveApp(input appInput, owner string) (*app, error) {
	if input.Launcher != "" {
		return nil, fmt.Errorf("请直接填写 startFile，不再接受 launcher")
	}
	if input.StartFile == "" && input.Address == "" {
		return nil, fmt.Errorf("请填写已有服务的 address 或本机程序的 startFile")
	}
	a, err := p.resolve(route{Owner: owner, Domain: input.Domain, Address: input.Address, StartFile: input.StartFile,
		Args: input.Args, AutoStart: input.AutoStart,
		routeInput: routeInput{Name: input.Name, Description: input.Description, Port: input.Port}})
	if err != nil {
		return nil, err
	}
	if a.local != nil {
		if err := validateStartFile(a.local.StartFile); err != nil {
			return nil, fmt.Errorf("托管应用 %q：%w", a.host, err)
		}
	}
	return a, nil
}

type app struct {
	route
	host    string
	target  url.URL
	status  atomic.Int32
	local   *localAppConfig
	process *localProcess
}

func (a *app) fileManaged() bool {
	return a.local != nil && a.Launcher == "" && a.StartFile == ""
}

func sameRoute(a, b route) bool {
	return slices.Equal(a.Args, b.Args) && a.Alias == b.Alias && a.Owner == b.Owner && a.Domain == b.Domain &&
		a.Address == b.Address && a.Launcher == b.Launcher && a.StartFile == b.StartFile && a.AutoStart == b.AutoStart && a.routeInput == b.routeInput
}

type catalog struct {
	policy *policy
	apps   []*app
	byHost map[string]*app
}

func newCatalog(p *policy, apps []*app) *catalog {
	c := &catalog{policy: p, apps: apps, byHost: make(map[string]*app, len(apps))}
	slices.SortFunc(apps, func(a, b *app) int { return strings.Compare(a.host, b.host) })
	for _, a := range apps {
		c.byHost[a.host] = a
	}
	return c
}

func (p *policy) resolve(r route) (*app, error) {
	if !validAlias.MatchString(r.Owner) {
		return nil, fmt.Errorf("非法所有者：%q", r.Owner)
	}
	if r.Launcher != "" || r.StartFile != "" {
		file, autoStart := r.StartFile, r.AutoStart
		if r.Launcher != "" {
			var ok bool
			file, ok = p.Launchers[r.Launcher]
			if !ok || r.StartFile != "" || len(r.Args) != 0 || r.AutoStart {
				return nil, fmt.Errorf("旧启动项不存在或与直接启动配置混用")
			}
			autoStart = true
		}
		if r.Alias != "" || r.Node != "" || r.Address != "" || r.Port < 0 || r.Port > 65535 {
			return nil, fmt.Errorf("本机托管不能混用上游地址或节点；端口不填或填写 1–65535")
		}
		a, err := p.resolveLocal(localAppConfig{Domain: r.Domain, StartFile: file, Args: r.Args, AutoStart: &autoStart,
			Port: uint16(r.Port), Name: r.Name, Description: r.Description})
		if err != nil {
			return nil, err
		}
		r.Domain, r.Name, r.Description = a.host, a.Name, a.Description
		a.route = r
		return a, nil
	}
	if len(r.Args) != 0 || r.AutoStart {
		return nil, fmt.Errorf("启动参数和自动启动仅用于本机托管")
	}
	var host, address string
	if r.Domain != "" || r.Address != "" {
		if r.Alias != "" || r.Node != "" || r.Port != 0 {
			return nil, fmt.Errorf("直接地址不能与节点配置混用")
		}
		var err error
		host, err = p.appDomain(r.Domain)
		if err != nil {
			return nil, err
		}
		addr, err := netip.ParseAddrPort(strings.TrimSpace(r.Address))
		if err != nil || addr.Port() == 0 || addr.Addr().Zone() != "" || addr.Addr().Is4In6() ||
			!(addr.Addr().IsPrivate() || addr.Addr().IsLoopback()) {
			return nil, fmt.Errorf("请填写私有或回环 IP 加端口，例如 127.0.0.1:1234 或 [::1]:1234")
		}
		if addr.Port() == p.listener.Port() {
			return nil, fmt.Errorf("上游不能使用本服务的监听端口")
		}
		address = addr.String()
		r.Domain, r.Address = host, address
	} else {
		if !validAlias.MatchString(r.Alias) {
			return nil, fmt.Errorf("非法别名：%q", r.Alias)
		}
		host = r.Alias + "." + p.BaseDomain
		n, ok := p.Nodes[r.Node]
		if !ok || !n.portRange.contains(r.Port) || r.Port == int(p.listener.Port()) {
			return nil, fmt.Errorf("目标节点或端口不在发布范围内")
		}
		address = net.JoinHostPort(n.IP, strconv.Itoa(r.Port))
	}
	if host == p.HomeHost {
		return nil, fmt.Errorf("%s 保留给导航首页", host)
	}
	r.Name, r.Description = strings.TrimSpace(r.Name), strings.TrimSpace(r.Description)
	if n := utf8.RuneCountInString(r.Name); n < 1 || n > 80 {
		return nil, fmt.Errorf("name 须为 1–80 个 Unicode 字符")
	}
	if utf8.RuneCountInString(r.Description) > 400 {
		return nil, fmt.Errorf("description 最多 400 个 Unicode 字符")
	}
	return &app{route: r, host: host, target: url.URL{
		Scheme: "http", Host: address,
	}}, nil
}

func (p *policy) appDomain(input string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(input))
	if !strings.Contains(domain, ".") && validLabel.MatchString(domain) {
		domain += "." + p.BaseDomain
	}
	if !dnsName(domain) || isIPHost(domain) {
		return "", fmt.Errorf("请填写完整域名或单个域名前缀，不含协议、端口和路径")
	}
	return domain, nil
}

func (p *policy) catalog(routes []route) (*catalog, error) {
	if len(routes)+len(p.LocalApps) > maxApps {
		return nil, fmt.Errorf("最多允许 %d 条路由", maxApps)
	}
	apps := make([]*app, 0, len(routes)+len(p.LocalApps))
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		a, err := p.resolve(r)
		if err != nil {
			return nil, fmt.Errorf("路由 %q：%w", r.Domain+r.Alias, err)
		}
		if seen[a.host] {
			return nil, fmt.Errorf("域名 %q 重复", a.host)
		}
		seen[a.host] = true
		apps = append(apps, a)
	}
	for _, spec := range p.LocalApps {
		a, err := p.resolveLocal(spec)
		if err != nil {
			return nil, err
		}
		if seen[a.host] {
			return nil, fmt.Errorf("域名 %q 重复", a.host)
		}
		seen[a.host] = true
		apps = append(apps, a)
	}
	return newCatalog(p, apps), nil
}
