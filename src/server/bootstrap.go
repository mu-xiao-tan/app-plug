package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func initAdmin(path, name, listenOverride string) (string, error) {
	// 读取和原子替换同一个目标，不会把配置的符号链接替换成普通文件。
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("定位配置文件：%w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("检查配置文件：%w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("配置路径必须指向普通文件")
	}

	p := defaultPolicy()
	if err := readJSON(path, maxPolicyBytes, &p); err != nil {
		return "", fmt.Errorf("读取配置文件：%w", err)
	}
	if _, exists := p.Principals[name]; exists {
		return "", fmt.Errorf("身份 %q 已存在", name)
	}
	for _, grant := range p.Principals {
		if grant.Admin {
			return "", errors.New("配置已包含管理员身份，不能再次初始化")
		}
	}

	token, digest, err := newAdminToken()
	if err != nil {
		return "", fmt.Errorf("生成管理员令牌：%w", err)
	}
	if p.Principals == nil {
		p.Principals = make(map[string]principal, 1)
	}
	p.Principals[name] = principal{Admin: true, TokenSHA256: digest}
	if err := p.validate(listenOverride); err != nil {
		return "", fmt.Errorf("配置无效：%w", err)
	}
	if err := writeJSONAtomic(path, &p, maxPolicyBytes); err != nil {
		return "", fmt.Errorf("写入配置文件：%w", err)
	}
	return token, nil
}

func newAdminToken() (token, digest string, err error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", err
	}
	var text [2 * len(random)]byte
	hex.Encode(text[:], random[:])
	hash := sha256.Sum256(text[:])
	token = string(text[:])
	hex.Encode(text[:], hash[:])
	return token, string(text[:]), nil
}
