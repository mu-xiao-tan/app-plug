package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxPolicyBytes = 256 << 10
	maxRouteBytes  = 8 << 10
)

const defaultListenPort = 49101

const (
	defaultAdminPath = "/admin"
	defaultAPIPath   = "/api"
	defaultMCPPath   = "/mcp"
)

var (
	validAlias = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	validLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

type policy struct {
	BaseDomain string               `json:"baseDomain"`
	HomeHost   string               `json:"homeHost"`
	ListenPort int                  `json:"listenPort"`
	APIMode    apiMode              `json:"apiMode"`
	AdminPath  string               `json:"adminPath"`
	APIPath    string               `json:"apiPath"`
	MCPPath    string               `json:"mcpPath"`
	MCPMode    apiMode              `json:"mcpMode"`
	Nodes      map[string]node      `json:"nodes"`
	LocalApps  []localAppConfig     `json:"localApps,omitempty"`
	Launchers  map[string]string    `json:"launchers,omitempty"`
	Principals map[string]principal `json:"principals"`
	listener   netip.AddrPort
	byToken    map[[sha256.Size]byte]string
}

type apiMode string

const (
	apiOff apiMode = "off"
	apiOn  apiMode = "on"
	apiIP  apiMode = "ip"
)

func (m *apiMode) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		switch mode := apiMode(value); mode {
		case apiOff, apiOn, apiIP:
			*m = mode
			return nil
		}
	}
	return errors.New("访问模式必须是 off、on 或 ip")
}

type portRange struct {
	MinPort int `json:"minPort"`
	MaxPort int `json:"maxPort"`
}

func (r portRange) valid() bool {
	return r.MinPort >= 1 && r.MaxPort <= 65535 && r.MinPort <= r.MaxPort
}
func (r portRange) contains(port int) bool { return r.MinPort <= port && port <= r.MaxPort }

type node struct {
	IP string `json:"ip"`
	portRange
}

type principal struct {
	TokenSHA256 string               `json:"tokenSHA256"`
	Admin       bool                 `json:"admin,omitempty"`
	Aliases     []string             `json:"aliases,omitempty"`
	Prefixes    []string             `json:"prefixes,omitempty"`
	Nodes       map[string]portRange `json:"nodes,omitempty"`
	Domains     []string             `json:"domains,omitempty"`
	Networks    []string             `json:"networks,omitempty"`
	Launchers   []string             `json:"launchers,omitempty"`
	networks    []netip.Prefix
}

func (p principal) allowsAlias(alias string) bool {
	if p.Admin {
		return true
	}
	for _, exact := range p.Aliases {
		if exact == alias {
			return true
		}
	}
	for _, prefix := range p.Prefixes {
		if strings.HasPrefix(alias, prefix) && len(alias) > len(prefix) {
			return true
		}
	}
	return false
}

func (p principal) allowsTarget(r routeInput) bool {
	ports, ok := p.Nodes[r.Node]
	return p.Admin || ok && ports.contains(r.Port)
}

func (p principal) allowsLauncher(name string) bool {
	return p.Admin || slices.Contains(p.Launchers, name)
}

func (p principal) allowsDomain(host, base string) bool {
	if p.Admin {
		return true
	}
	if alias, ok := strings.CutSuffix(host, "."+base); ok && validAlias.MatchString(alias) && p.allowsAlias(alias) {
		return true
	}
	for _, domain := range p.Domains {
		if domain == host {
			return true
		}
		if suffix, wildcard := strings.CutPrefix(domain, "*"); wildcard {
			if label, ok := strings.CutSuffix(host, suffix); ok && validLabel.MatchString(label) {
				return true
			}
		}
	}
	return false
}

func (p *policy) allowsAddress(grant principal, addr netip.AddrPort) bool {
	if grant.Admin {
		return true
	}
	for _, network := range grant.networks {
		if network.Contains(addr.Addr()) {
			return true
		}
	}
	// 原有节点授权仍只允许原来指定的 IP 和端口。
	for name, ports := range grant.Nodes {
		nodeIP, _ := netip.ParseAddr(p.Nodes[name].IP)
		if nodeIP == addr.Addr() && ports.contains(int(addr.Port())) {
			return true
		}
	}
	return false
}

func isInternalNetwork(prefix netip.Prefix) bool {
	ip := prefix.Masked().Addr()
	if ip.Is4() {
		switch octet := ip.As4(); octet[0] {
		case 10, 127:
			return prefix.Bits() >= 8
		case 172:
			return prefix.Bits() >= 12 && octet[1] >= 16 && octet[1] <= 31
		case 192:
			return prefix.Bits() >= 16 && octet[1] == 168
		}
		return false
	}
	return !ip.Is4In6() && (ip.IsPrivate() && prefix.Bits() >= 7 || ip.IsLoopback() && prefix.Bits() == 128)
}

func dnsName(s string) bool {
	if len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if !validLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func loadPolicy(path, listenOverride string) (*policy, error) {
	p := defaultPolicy()
	if err := readJSON(path, maxPolicyBytes, &p); err != nil {
		return nil, err
	}
	if err := p.validate(listenOverride); err != nil {
		return nil, err
	}
	return &p, nil
}

func defaultPolicy() policy {
	return policy{ListenPort: defaultListenPort, APIMode: apiIP, AdminPath: defaultAdminPath, APIPath: defaultAPIPath, MCPPath: defaultMCPPath, MCPMode: apiIP}
}

func hasPathPrefix(path, prefix string) bool {
	return path == prefix || len(path) > len(prefix) && path[len(prefix)] == '/' && strings.HasPrefix(path, prefix)
}

func (p *policy) validate(listenOverride string) error {
	if !validAlias.MatchString(strings.TrimPrefix(p.AdminPath, "/")) || !strings.HasPrefix(p.AdminPath, "/") {
		return errors.New("adminPath 必须为 / 加单段小写别名，例如 /manage")
	}
	if !validAlias.MatchString(strings.TrimPrefix(p.APIPath, "/")) || !strings.HasPrefix(p.APIPath, "/") {
		return errors.New("apiPath 必须为 / 加单段小写别名，例如 /abc")
	}
	if !validAlias.MatchString(strings.TrimPrefix(p.MCPPath, "/")) || !strings.HasPrefix(p.MCPPath, "/") {
		return errors.New("mcpPath 必须为 / 加单段小写别名，例如 /mcp")
	}
	if p.AdminPath == p.APIPath || p.AdminPath == p.MCPPath || p.APIPath == p.MCPPath {
		return errors.New("adminPath、apiPath 和 mcpPath 必须互不相同")
	}
	if p.ListenPort < 1 || p.ListenPort > 65535 {
		return errors.New("listenPort 必须是 1–65535 的端口")
	}
	p.listener = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(p.ListenPort))
	if listenOverride != "" {
		addr, err := netip.ParseAddrPort(listenOverride)
		if err != nil || addr.Port() == 0 || addr.Addr().Zone() != "" {
			return errors.New("-listen 必须使用 IP 和 1–65535 的端口")
		}
		p.listener = addr
	}
	if !dnsName(p.BaseDomain) || len(p.BaseDomain) > 220 || !dnsName(p.HomeHost) {
		return errors.New("baseDomain 和 homeHost 必须是小写 DNS 域名")
	}
	if p.HomeHost != p.BaseDomain {
		label, ok := strings.CutSuffix(p.HomeHost, "."+p.BaseDomain)
		if !ok || !validAlias.MatchString(label) {
			return errors.New("homeHost 必须是 baseDomain 或其单级应用子域名")
		}
	}
	for name, n := range p.Nodes {
		ip, err := netip.ParseAddr(n.IP)
		if !validAlias.MatchString(name) || err != nil || ip.Zone() != "" || ip.Is4In6() ||
			!(ip.IsPrivate() || ip.IsLoopback()) || !n.portRange.valid() {
			return fmt.Errorf("节点 %q 须使用私有或回环 IP 及合法端口范围", name)
		}
	}
	for name, file := range p.Launchers {
		if !validAlias.MatchString(name) {
			return fmt.Errorf("启动项名称无效：%q", name)
		}
		if err := validateStartPath(file); err != nil {
			return fmt.Errorf("启动项 %q：%w", name, err)
		}
	}
	p.Principals = maps.Clone(p.Principals)
	p.byToken = make(map[[sha256.Size]byte]string, len(p.Principals))
	var hasAdmin bool
	for name, grant := range p.Principals {
		var digest [sha256.Size]byte
		if !validAlias.MatchString(name) || len(grant.TokenSHA256) != hex.EncodedLen(len(digest)) {
			return fmt.Errorf("身份 %q 的名称或令牌摘要无效", name)
		}
		if _, err := hex.Decode(digest[:], []byte(grant.TokenSHA256)); err != nil {
			return fmt.Errorf("身份 %q 的令牌摘要无效", name)
		}
		if _, duplicate := p.byToken[digest]; duplicate {
			return errors.New("不同身份不能共用令牌")
		}
		p.byToken[digest] = name
		if grant.Admin {
			if len(grant.Aliases)+len(grant.Prefixes)+len(grant.Nodes)+len(grant.Domains)+len(grant.Networks)+len(grant.Launchers) != 0 {
				return fmt.Errorf("管理员 %q 不应配置局部权限", name)
			}
			hasAdmin = true
			continue
		}
		if len(grant.Aliases)+len(grant.Prefixes)+len(grant.Domains) == 0 || len(grant.Nodes)+len(grant.Networks)+len(grant.Launchers) == 0 {
			return fmt.Errorf("身份 %q 需要域名和目标地址或启动项授权", name)
		}
		for _, alias := range grant.Aliases {
			if !validAlias.MatchString(alias) || alias+"."+p.BaseDomain == p.HomeHost {
				return fmt.Errorf("身份 %q 的别名授权无效：%q", name, alias)
			}
		}
		for _, prefix := range grant.Prefixes {
			label, ok := strings.CutSuffix(prefix, "-")
			if !ok || len(prefix) >= 32 || !validAlias.MatchString(label) {
				return fmt.Errorf("身份 %q 的前缀须以 - 结尾且可追加别名字符", name)
			}
		}
		for nodeName, ports := range grant.Nodes {
			n, ok := p.Nodes[nodeName]
			if !ok || !ports.valid() || !n.portRange.contains(ports.MinPort) || !n.portRange.contains(ports.MaxPort) {
				return fmt.Errorf("身份 %q 对节点 %q 的端口授权超出发布范围", name, nodeName)
			}
		}
		for _, name := range grant.Launchers {
			if _, ok := p.Launchers[name]; !ok {
				return fmt.Errorf("身份 %q 引用了未登记的启动项 %q", name, name)
			}
		}
		grant.networks = nil
		for _, domain := range grant.Domains {
			host := strings.TrimPrefix(domain, "*.")
			if !dnsName(host) || isIPHost(host) || domain == p.HomeHost {
				return fmt.Errorf("身份 %q 的域名授权无效：%q", name, domain)
			}
		}
		for _, value := range grant.Networks {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				ip, err := netip.ParseAddr(value)
				if err != nil || ip.Zone() != "" {
					return fmt.Errorf("身份 %q 的 IP 或网段无效：%q", name, value)
				}
				prefix = netip.PrefixFrom(ip, ip.BitLen())
			}
			if !isInternalNetwork(prefix) {
				return fmt.Errorf("身份 %q 的授权必须是私有或回环 IP 或网段：%q", name, value)
			}
			grant.networks = append(grant.networks, prefix)
		}
		p.Principals[name] = grant
	}
	if !hasAdmin {
		return errors.New("至少配置一个管理员身份")
	}
	return nil
}

func readJSON(path string, limit int64, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return decodeJSON(f, limit, dst)
}

func decodeJSON(r io.Reader, limit int64, dst any) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errJSONTooLarge
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) || !utf8.Valid(data) {
		return errors.New("需要非空的 UTF-8 JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return errors.New("只允许一个 JSON 值")
	}
	return nil
}

var errJSONTooLarge = errors.New("JSON 超出大小限制")
