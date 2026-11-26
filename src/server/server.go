package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Version 是 CLI、页面和 MCP 共同报告的发布版本。
const Version = "v0.15.0"

// Run 处理前台服务参数；帮助输出包含同一可执行文件的 CLI 命令。
func Run(printHelp func(io.Writer)) error {
	listen := flag.String("listen", "", "覆盖监听地址，格式 IP:端口；省略时使用 config.json 的 listenPort 和回环地址")
	config := flag.String("config", "./config.json", "策略 JSON 文件（域名、访问模式、身份和授权）")
	stateDir := flag.String("state-dir", "./state", "已存在的路由状态目录，包含 routes.json 和实例锁")
	check := flag.Bool("check-config", false, "仅校验策略和路由，不监听或启动应用")
	bootstrap := flag.String("init-admin", "", "在已有配置中首次生成管理员令牌，值为身份名；仅向标准输出显示一次")
	flag.Usage = func() {
		printHelp(os.Stderr)
		fmt.Fprintln(os.Stderr, "\n前台服务参数：")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("不接受额外参数")
	}
	if *check && *bootstrap != "" {
		return errors.New("-check-config 和 -init-admin 不能一起使用")
	}
	info, err := os.Stat(*stateDir)
	if err != nil {
		return fmt.Errorf("读取路由目录：%w", err)
	}
	if !info.IsDir() {
		return errors.New("-state-dir 必须是目录")
	}
	if !*check {
		lock, err := lockState(*stateDir)
		if err != nil {
			return fmt.Errorf("无法锁定路由目录：%w", err)
		}
		defer lock.Close()
	}
	if *bootstrap != "" {
		token, err := initAdmin(*config, *bootstrap, *listen)
		if err != nil {
			return fmt.Errorf("初始化管理员失败：%w", err)
		}
		fmt.Fprintln(os.Stdout, token)
		return nil
	}
	p, err := loadPolicy(*config, *listen)
	if err != nil {
		return fmt.Errorf("加载策略失败：%w", err)
	}
	c, err := loadRoutes(*stateDir, p)
	if err != nil {
		return fmt.Errorf("加载路由失败：%w", err)
	}
	if *check {
		log.Printf("配置有效：%d 条路由", len(c.apps))
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hup, stopReload := configReloadSignal()
	defer stopReload()

	s := &routeStore{dir: *stateDir, configPath: *config, listenOverride: *listen, changed: make(chan struct{}, 1)}
	listener, err := net.Listen("tcp", p.listener.String())
	if err != nil {
		return fmt.Errorf("无法监听应用入口：%w", err)
	}
	defer listener.Close()
	s.publish(c)
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.locals.close()
	}()
	n := newNavigator(s)
	srv := &http.Server{Handler: n, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go n.runProbes(ctx)
	go func() { done <- srv.Serve(listener) }()
	log.Printf("应用入口 %s；管理后台 %s；管理 API %s；MCP %s（%s）；已加载 %d 条路由", listener.Addr(), p.AdminPath, p.APIPath, p.MCPPath, p.MCPMode, len(c.apps))
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdown)
		case <-hup:
			if err := s.reload(); err != nil {
				log.Printf("重载失败，保留上一份配置：%v", err)
				continue
			}
			p := s.current.Load().policy
			log.Printf("已重载入口策略和路由；管理后台 %s；管理 API %s；MCP %s（%s）", p.AdminPath, p.APIPath, p.MCPPath, p.MCPMode)
		}
	}
}
