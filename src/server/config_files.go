package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ConfigSettings 包含 config init 和 set 支持的策略字段。
// 身份、节点与文件声明的路由只能通过完整导入替换。
type ConfigSettings struct {
	BaseDomain string
	HomeHost   string
	ListenPort int
	AdminPath  string
	APIPath    string
	MCPPath    string
	APIMode    string
	MCPMode    string
}

func (p *policy) settings() ConfigSettings {
	return ConfigSettings{
		BaseDomain: p.BaseDomain,
		HomeHost:   p.HomeHost,
		ListenPort: p.ListenPort,
		AdminPath:  p.AdminPath,
		APIPath:    p.APIPath,
		MCPPath:    p.MCPPath,
		APIMode:    string(p.APIMode),
		MCPMode:    string(p.MCPMode),
	}
}

func (p *policy) setSettings(settings ConfigSettings) {
	p.BaseDomain = settings.BaseDomain
	p.HomeHost = settings.HomeHost
	p.ListenPort = settings.ListenPort
	p.AdminPath = settings.AdminPath
	p.APIPath = settings.APIPath
	p.MCPPath = settings.MCPPath
	p.APIMode = apiMode(settings.APIMode)
	p.MCPMode = apiMode(settings.MCPMode)
}

// DefaultConfigSettings 返回加载策略文件时使用的默认设置。
func DefaultConfigSettings() ConfigSettings {
	p := defaultPolicy()
	return p.settings()
}

// ShowConfig 输出有效策略，隐藏所有身份的令牌摘要。
func ShowConfig(config, listen string, out io.Writer) error {
	p, err := loadPolicy(config, listen)
	if err != nil {
		return err
	}
	redacted := struct {
		*policy
		Principals map[string]consolePerson `json:"principals"`
	}{policy: p, Principals: make(map[string]consolePerson, len(p.Principals))}
	for name, grant := range p.Principals {
		redacted.Principals[name] = consolePerson{
			Admin: grant.Admin, Aliases: grant.Aliases, Prefixes: grant.Prefixes, Nodes: grant.Nodes,
			Domains: grant.Domains, Networks: grant.Networks, Launchers: grant.Launchers,
		}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(redacted)
}

// CheckConfig 校验策略和路由，不启动监听，也不锁定状态目录。
// 返回路由数量和规范化的服务监听地址。
func CheckConfig(config, state, listen string) (routeCount int, canonicalListen string, err error) {
	if err := checkConfigStateDir(state); err != nil {
		return 0, "", err
	}
	p, err := loadPolicy(config, listen)
	if err != nil {
		return 0, "", fmt.Errorf("加载策略失败：%w", err)
	}
	count, err := checkConfigRoutes(state, p)
	if err != nil {
		return 0, "", err
	}
	return count, p.listener.String(), nil
}

// InitConfig 创建策略和私有管理员令牌文件，不覆盖任何已有文件。
func InitConfig(config, state, tokenPath, admin, listen string, settings ConfigSettings) error {
	p := defaultPolicy()
	p.setSettings(settings)
	token, digest, err := newAdminToken()
	if err != nil {
		return err
	}
	p.Principals = map[string]principal{admin: {Admin: true, TokenSHA256: digest}}
	if err := validateConfigSettings(&p, listen); err != nil {
		return err
	}
	for _, path := range []string{config, tokenPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%s 已存在，不覆盖", path)
		}
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	if _, err := loadRoutes(state, &p); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&p, "", "  ")
	if err != nil {
		return err
	}
	for _, dir := range []string{filepath.Dir(config), filepath.Dir(tokenPath)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	if err := createPrivateConfigFile(tokenPath, []byte(token+"\n")); err != nil {
		return err
	}
	if err := createPrivateConfigFile(config, append(data, '\n')); err != nil {
		return errors.Join(err, os.Remove(tokenPath))
	}
	return nil
}

// SetConfig 只修改指定的标量字段，校验路由后原子保存。
func SetConfig(config, state, listen string, update func(*ConfigSettings) error) (routeCount int, err error) {
	path, _, err := configTarget(config)
	if err != nil {
		return 0, err
	}
	p := defaultPolicy()
	if err := readJSON(path, maxPolicyBytes, &p); err != nil {
		return 0, err
	}
	settings := p.settings()
	if err := update(&settings); err != nil {
		return 0, err
	}
	p.setSettings(settings)
	if err := validateConfigSettings(&p, listen); err != nil {
		return 0, err
	}
	return saveConfig(path, state, &p)
}

// ApplyConfig 从另一文件加载完整策略，校验后原子替换现有策略。
func ApplyConfig(config, state, listen, from string) (routeCount int, err error) {
	path, info, err := configTarget(config)
	if err != nil {
		return 0, err
	}
	source, err := os.Stat(from)
	if err != nil {
		return 0, err
	}
	if !source.Mode().IsRegular() || os.SameFile(info, source) {
		return 0, errors.New("--from 必须是不同于 --config 的普通文件")
	}
	p, err := loadPolicy(from, listen)
	if err != nil {
		return 0, err
	}
	return saveConfig(path, state, p)
}

func configTarget(config string) (string, os.FileInfo, error) {
	path, err := filepath.EvalSymlinks(config)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("--config 必须指向普通文件")
	}
	return path, info, nil
}

func validateConfigSettings(p *policy, listen string) error {
	for _, mode := range []apiMode{p.APIMode, p.MCPMode} {
		if mode != apiIP && mode != apiOn && mode != apiOff {
			return errors.New("访问模式必须是 ip、on 或 off")
		}
	}
	return p.validate(listen)
}

func checkConfigStateDir(state string) error {
	info, err := os.Stat(state)
	if err != nil {
		return fmt.Errorf("读取路由目录：%w", err)
	}
	if !info.IsDir() {
		return errors.New("--state-dir 必须是目录")
	}
	return nil
}

func checkConfigRoutes(state string, p *policy) (int, error) {
	c, err := loadRoutes(state, p)
	if err != nil {
		return 0, fmt.Errorf("加载路由失败：%w", err)
	}
	return len(c.apps), nil
}

func saveConfig(path, state string, p *policy) (int, error) {
	if err := checkConfigStateDir(state); err != nil {
		return 0, err
	}
	count, err := checkConfigRoutes(state, p)
	if err != nil {
		return 0, err
	}
	if err := writeJSONAtomic(path, p, maxPolicyBytes); err != nil {
		return 0, err
	}
	return count, nil
}

func createPrivateConfigFile(path string, data []byte) error {
	f, err := openPrivateConfig(path)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}
