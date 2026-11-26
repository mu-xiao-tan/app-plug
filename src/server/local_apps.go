package server

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type localAppConfig struct {
	Domain      string   `json:"domain"`
	StartFile   string   `json:"startFile"`
	Port        uint16   `json:"port,omitempty"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Args        []string `json:"args,omitempty"`
	AutoStart   *bool    `json:"autoStart,omitempty"`
}

func (p *policy) resolveLocal(spec localAppConfig) (*app, error) {
	host, err := p.appDomain(spec.Domain)
	if err != nil {
		return nil, err
	}
	if host == p.HomeHost || spec.Port == p.listener.Port() {
		return nil, fmt.Errorf("托管应用 %q 不能使用导航域名或监听端口", host)
	}
	if err := validateStartPath(spec.StartFile); err != nil {
		return nil, fmt.Errorf("托管应用 %q：%w", host, err)
	}
	for _, arg := range spec.Args {
		if strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("启动参数不能含 NUL 字符")
		}
	}
	spec.Name, spec.Description = strings.TrimSpace(spec.Name), strings.TrimSpace(spec.Description)
	if spec.Name == "" {
		spec.Name = host
	}
	if utf8.RuneCountInString(spec.Name) > 80 || utf8.RuneCountInString(spec.Description) > 400 {
		return nil, fmt.Errorf("托管应用 %q 的名称或简介过长", host)
	}
	return &app{host: host, local: &spec, route: route{routeInput: routeInput{Name: spec.Name, Description: spec.Description}}}, nil
}

func validateStartPath(file string) error {
	if !filepath.IsAbs(file) {
		return fmt.Errorf("startFile 必须是绝对路径")
	}
	if strings.ContainsRune(file, 0) {
		return fmt.Errorf("startFile 不能含 NUL 字符")
	}
	return nil
}

// 文件是否可执行属于运行条件，不影响已保存配置的加载和校验。
func validateStartFile(file string) error {
	info, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("读取启动文件：%w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("启动文件须为普通文件")
	}
	return validatePlatformExecutable(file, info)
}

// 调用者持有 routeStore 的写锁；进程和路由快照的引用随写入一起更新。
type localManager struct {
	apps   map[string]managedLocal
	closed bool
}

type managedLocal struct {
	config  *localAppConfig
	process *localProcess
}

func (c *localAppConfig) startsAutomatically() bool {
	return c.AutoStart == nil || *c.AutoStart
}

func (c *localAppConfig) sameCommand(other *localAppConfig) bool {
	return c.StartFile == other.StartFile && c.Port == other.Port && slices.Equal(c.Args, other.Args)
}

func (m *localManager) apply(c *catalog, changed chan<- struct{}) {
	if m.closed {
		return
	}
	type stoppedLocal struct {
		process *localProcess
		restart bool
	}
	var stops map[string]stoppedLocal
	// 先停止所有移除或更换命令的应用，再启动新进程；允许同批次交换端口。
	for host, entry := range m.apps {
		a := c.byHost[host]
		removed := a == nil || a.local == nil
		if !removed && entry.config.sameCommand(a.local) {
			continue
		}
		if p := entry.process; p != nil {
			if stops == nil {
				stops = make(map[string]stoppedLocal)
			}
			stops[host] = stoppedLocal{process: p, restart: p.stop() && !removed}
		}
		if removed {
			delete(m.apps, host)
		} else {
			entry.process = nil
			m.apps[host] = entry
		}
	}
	for _, old := range stops {
		old.process.join()
	}
	for i, a := range c.apps {
		if a.local == nil {
			continue
		}
		if m.apps == nil {
			m.apps = make(map[string]managedLocal)
		}
		entry, exists := m.apps[a.host]
		entry.config = a.local
		if (!exists && a.local.startsAutomatically()) || stops[a.host].restart {
			entry.process, _ = startLocal(a.host, *a.local, c.policy.listener.Port(), changed)
		}
		m.apps[a.host] = entry
		if a.process != entry.process {
			next := &app{route: a.route, host: a.host, local: a.local, process: entry.process}
			c.apps[i], c.byHost[a.host] = next, next
		}
	}
}

func (m *localManager) close() {
	m.closed = true
	for _, entry := range m.apps {
		if entry.process != nil {
			entry.process.stop()
		}
	}
	for _, entry := range m.apps {
		if entry.process != nil {
			entry.process.join()
		}
	}
	m.apps = nil
}

func (m *localManager) stop(host string) {
	if p := m.apps[host].process; p != nil {
		p.stop()
		p.join()
	}
}

func (p *localProcess) running() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid != 0 && !p.stopping
}

type localProcess struct {
	platformLocalProcess
	cancelDiscovery context.CancelFunc
	port            uint16
	mu              sync.Mutex
	pid             int
	stopping        bool
	done            chan struct{}
	target          atomic.Pointer[url.URL]
}

func startLocal(host string, config localAppConfig, entryPort uint16, changed chan<- struct{}) (*localProcess, error) {
	if err := validateStartFile(config.StartFile); err != nil {
		log.Printf("托管应用 %s 启动失败：%v", host, err)
		return nil, err
	}
	p := &localProcess{port: config.Port, done: make(chan struct{})}
	cmd := exec.Command(config.StartFile, config.Args...)
	cmd.Dir = filepath.Dir(config.StartFile)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	wait, err := p.start(cmd)
	if err != nil {
		log.Printf("托管应用 %s 启动失败：%v", host, err)
		return nil, err
	}
	p.pid = cmd.Process.Pid
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelDiscovery = cancel
	discoveryDone := make(chan struct{})
	go func() {
		defer close(discoveryDone)
		p.discover(ctx, host, entryPort, changed)
	}()
	go func() {
		defer close(p.done)
		wait(host)
		cancel()
		<-discoveryDone
	}()
	return p, nil
}

func (p *localProcess) clearLocked() bool {
	p.target.Store(nil)
	p.pid = 0
	return p.stopping
}

func (p *localProcess) stop() bool {
	if p.cancelDiscovery != nil {
		p.cancelDiscovery()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	wasRunning := p.pid != 0 && !p.stopping
	p.stopping = true
	p.target.Store(nil)
	if p.pid != 0 {
		p.terminateLocked(false)
	}
	return wasRunning
}

func (p *localProcess) join() {
	select {
	case <-p.done:
		return
	case <-time.After(2 * time.Second):
		p.mu.Lock()
		if p.pid != 0 {
			p.terminateLocked(true)
		}
		p.mu.Unlock()
		<-p.done
	}
}

func (p *localProcess) discover(ctx context.Context, host string, entryPort uint16, changed chan<- struct{}) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var warned bool
	for {
		p.mu.Lock()
		if p.pid == 0 || p.stopping || ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		listeners, err := p.listeners(ctx, p.port)
		if ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		if err != nil && !warned {
			log.Printf("托管应用 %s 无法确认监听归属：%v", host, err)
		}
		warned = err != nil
		listeners = slices.DeleteFunc(listeners, func(addr netip.AddrPort) bool { return addr.Port() == entryPort })
		addr := chooseLocalListener(listeners)
		old := p.target.Load()
		if err != nil || !addr.IsValid() {
			if old != nil {
				p.target.Store(nil)
			}
		} else if old == nil || old.Host != addr.String() {
			p.target.Store(&url.URL{Scheme: "http", Host: addr.String()})
			log.Printf("托管应用 %s 已发现监听地址 %s", host, addr)
			select {
			case changed <- struct{}{}:
			default:
			}
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func chooseLocalListener(listeners []netip.AddrPort) netip.AddrPort {
	if len(listeners) == 0 {
		return netip.AddrPort{}
	}
	addr := listeners[0]
	for _, candidate := range listeners[1:] {
		if candidate.Port() != addr.Port() {
			return netip.AddrPort{}
		}
		if candidate.Addr().Is4() {
			addr = candidate
		}
	}
	return addr
}

func (a *app) upstream() *url.URL {
	if a.local == nil {
		return &a.target
	}
	if a.process == nil {
		return nil
	}
	return a.process.target.Load()
}
