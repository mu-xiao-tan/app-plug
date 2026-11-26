package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const maxStateBytes = (maxApps + 1) * maxRouteBytes

// 写入、重载共用同一把锁；请求和探测只读取完整快照。
type routeStore struct {
	dir                        string
	configPath, listenOverride string
	current                    atomic.Pointer[catalog]
	mu                         sync.Mutex
	changed                    chan struct{}
	locals                     localManager
	deployKeys                 map[[32]byte]deployKey
}

func loadRoutes(dir string, p *policy) (*catalog, error) {
	var routes []route
	if err := readJSON(filepath.Join(dir, "routes.json"), maxStateBytes, &routes); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取路由状态：%w", err)
	}
	return p.catalog(routes)
}

// 同一状态目录只允许一个运行实例，避免跨进程覆盖路由。
func lockState(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockStateFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *routeStore) publish(next *catalog) {
	s.locals.apply(next, s.changed)
	s.current.Store(next)
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *routeStore) publishPolicy(next *catalog) {
	// 策略一旦使签发人失效，即使稍后恢复，也不能复活旧密钥。
	if len(s.deployKeys) != 0 {
		s.pruneDeployKeys(next.policy, time.Now())
	}
	s.publish(next)
}

func (s *routeStore) reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := loadPolicy(s.configPath, s.listenOverride)
	if err != nil {
		return err
	}
	if p.listener != s.current.Load().policy.listener {
		return errors.New("监听地址或端口变更需要重启")
	}
	next, err := loadRoutes(s.dir, p)
	if err != nil {
		return err
	}
	s.publishPolicy(next)
	return nil
}

// storedRoutes 排除由配置文件声明的应用，避免把同一条路由保存两次。
func (c *catalog) storedRoutes() []route {
	routes := make([]route, 0, len(c.apps))
	for _, a := range c.apps {
		if a.fileManaged() {
			continue
		}
		routes = append(routes, a.route)
	}
	return routes
}

func (s *routeStore) save(next *catalog) error {
	if err := writeJSONAtomic(filepath.Join(s.dir, "routes.json"), next.storedRoutes(), maxStateBytes); err != nil {
		return err
	}
	s.publish(next)
	return nil
}

func writeJSONAtomic(path string, value any, limit int64) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data))+1 > limit {
		return errJSONTooLarge
	}
	dirPath := filepath.Dir(path)
	f, err := privateConfigTemp(dirPath)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return commitConfigFile(dirPath, f.Name(), path)
}
