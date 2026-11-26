package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mu-xiao-tan/app-plug/src/server"
)

func runConfigCLI(args []string) error {
	applyHint := "修改后向前台进程发送 SIGHUP，或重启；监听地址或端口变更必须重启。"
	switch runtime.GOOS {
	case "linux":
		applyHint = "修改后可重载；监听地址或端口变更必须重启。用户级 systemd 服务可用 service reload 或 restart。"
	case "windows":
		applyHint = "修改后重启前台服务。"
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return cliGroupHelp("config", "在本机操作策略文件；"+applyHint,
			"init        初始化配置、管理员和状态目录（不覆盖已有文件）", "show        查看有效配置（隐藏令牌摘要）",
			"check       校验策略和路由，不启动服务", "set         修改域名、端口、管理路径和访问模式",
			"apply       导入完整策略 JSON，校验后原子应用（含身份、节点及本机文件路由）")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".apps", "app-plug")
	command := args[0]
	var description string
	switch command {
	case "init":
		description = "创建配置、状态目录和初始管理员令牌文件；令牌仅写入新建的私有文件。"
	case "show":
		description = "输出有效策略 JSON；不会显示身份的令牌摘要。"
	case "check":
		description = "校验策略和整个状态目录的路由，不开启监听、不启动应用。"
	case "set":
		description = "只修改显式传入的字段；校验已有路由后原子保存。" + applyHint
	case "apply":
		description = "从独立 JSON 文件载入完整策略；校验与已有路由的兼容性后替换目标配置。身份摘要由输入文件提供。"
	default:
		return fmt.Errorf("未知配置命令 %q，请查看 config --help", command)
	}
	fs := cliFlags("config "+command+" [参数]", description)
	config := fs.String("config", filepath.Join(root, "config.json"), "目标策略 JSON 文件；set/apply 保留符号链接本身")
	listen := fs.String("listen", "", "按服务实际监听覆盖校验，格式 IP:端口；不写入配置")
	var state *string
	if command != "show" {
		state = fs.String("state-dir", filepath.Join(root, "state"), "路由状态目录；仅 init 可创建")
	}
	settings := server.DefaultConfigSettings()
	if command == "init" || command == "set" {
		fs.StringVar(&settings.BaseDomain, "base-domain", settings.BaseDomain, "应用短名称追加的 DNS 后缀，如 example.com")
		fs.StringVar(&settings.HomeHost, "home-host", settings.HomeHost, "导航的完整域名，如 nav.example.com；init 省略时使用 nav.<基础域名>")
		fs.IntVar(&settings.ListenPort, "listen-port", settings.ListenPort, "未指定 -listen 时使用的端口 1–65535；实际监听端口改变需重启")
		fs.StringVar(&settings.AdminPath, "admin-path", settings.AdminPath, "后台路径，/ 加单段小写别名，不带尾斜杠")
		fs.StringVar(&settings.APIPath, "api-path", settings.APIPath, "REST 路径前缀，/ 加单段小写别名，不能与后台或 MCP 相同")
		fs.StringVar(&settings.MCPPath, "mcp-path", settings.MCPPath, "MCP 独立端点，/ 加单段小写别名，不能与后台或 REST 相同")
		fs.StringVar(&settings.APIMode, "api-mode", settings.APIMode, "REST 访问：ip（IP Host）、on（也允许导航域名）、off（关闭 REST）")
		fs.StringVar(&settings.MCPMode, "mcp-mode", settings.MCPMode, "MCP 访问：ip（IP Host）、on（也允许导航域名）、off（关闭 MCP）")
	}
	var admin, tokenFile, from *string
	if command == "init" {
		admin = fs.String("admin", "operator", "首个管理员身份名，以小写字母开头，允许数字和内部连字符")
		tokenFile = fs.String("token-file", "", "新建管理员令牌文件，默认放在策略文件旁的 admin-token；绝不覆盖")
	}
	if command == "apply" {
		from = fs.String("from", "", "必填，独立的完整策略 JSON 文件；必须与 --config 不同")
	}
	if err := parseCLI(fs, args[1:]); err != nil {
		return err
	}
	var count int
	switch command {
	case "show":
		return server.ShowConfig(*config, *listen, os.Stdout)
	case "check":
		count, _, err = server.CheckConfig(*config, *state, *listen)
	case "init":
		if settings.BaseDomain == "" {
			return errors.New("必须填写 --base-domain")
		}
		if settings.HomeHost == "" {
			settings.HomeHost = "nav." + settings.BaseDomain
		}
		if *tokenFile == "" {
			*tokenFile = filepath.Join(filepath.Dir(*config), "admin-token")
		}
		if err := server.InitConfig(*config, *state, *tokenFile, *admin, *listen, settings); err != nil {
			return err
		}
		fmt.Printf("已创建配置 %s；管理员令牌保存在 %s。\n", *config, *tokenFile)
		return nil
	case "apply":
		if *from == "" {
			return errors.New("必须填写 --from")
		}
		count, err = server.ApplyConfig(*config, *state, *listen, *from)
	case "set":
		count, err = server.SetConfig(*config, *state, *listen, func(current *server.ConfigSettings) error {
			return updateConfigSettings(fs, current, settings)
		})
	}
	if err != nil {
		return err
	}
	fmt.Printf("配置有效：%d 条路由\n", count)
	if command != "check" {
		fmt.Printf("已保存 %s；%s\n", *config, applyHint)
	}
	return nil
}

func updateConfigSettings(fs *flag.FlagSet, current *server.ConfigSettings, settings server.ConfigSettings) error {
	changed := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "base-domain":
			current.BaseDomain = settings.BaseDomain
		case "home-host":
			current.HomeHost = settings.HomeHost
		case "listen-port":
			current.ListenPort = settings.ListenPort
		case "admin-path":
			current.AdminPath = settings.AdminPath
		case "api-path":
			current.APIPath = settings.APIPath
		case "mcp-path":
			current.MCPPath = settings.MCPPath
		case "api-mode":
			current.APIMode = settings.APIMode
		case "mcp-mode":
			current.MCPMode = settings.MCPMode
		default:
			return
		}
		changed = true
	})
	if !changed {
		return errors.New("请至少指定一个要改的配置字段")
	}
	return nil
}
