package cli

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/mu-xiao-tan/app-plug/src/server"
)

const (
	serviceDefaultUnit = "app-plug.service"
	serviceUnitMarker  = "# 由 app-plug service install 生成\n"
	serviceUnitHead    = `[Unit]
Description=应用插座 · App Plug
After=network.target

[Service]
`
	serviceUnitTail = `
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=2s
NoNewPrivileges=true
UMask=0077

[Install]
WantedBy=default.target
`
)

var (
	serviceUnitName = regexp.MustCompile(`^app-plug[A-Za-z0-9_.:-]*\.service$`)
	// 只认领本命令使用的启动参数格式，不把任意标有注释的 unit 当作自有文件。
	serviceGeneratedExec = regexp.MustCompile(`^:"/([^"\\%\x00-\x1f\x7f]|\\["\\]|\\x[0-9a-f]{2}|%%)*" -config "/([^"\\%\x00-\x1f\x7f]|\\["\\]|\\x[0-9a-f]{2}|%%)*" -state-dir "/([^"\\%\x00-\x1f\x7f]|\\["\\]|\\x[0-9a-f]{2}|%%)*"( -listen "[a-fA-F0-9.:\[\]]+")?$`)
)

func runServiceCLI(args []string) error {
	if runtime.GOOS != "linux" {
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
			return cliGroupHelp("service", "用户级 systemd 服务管理仅支持 Linux；本平台可直接前台运行 app-plug -config <文件> -state-dir <目录>。")
		}
		return fmt.Errorf("%s 不支持用户级 systemd 服务操作；请前台运行 app-plug -config <文件> -state-dir <目录>", runtime.GOOS)
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return cliGroupHelp("service", "管理用户级 systemd 服务；每个命令均支持 --unit 指定实例。",
			"install     注册当前二进制并启用登录自启，可选立即启动",
			"start       启动服务", "stop        停止服务（保留登录自启）", "restart     重启服务",
			"reload      向服务发送 SIGHUP，重载策略与路由", "status      显示 systemd 服务状态",
			"uninstall   停止服务、取消自启并移除 unit 注册（保留配置、状态与二进制）")
	}
	command := args[0]
	var description string
	switch command {
	case "install":
		description = "校验现有策略和状态，注册当前二进制并启用登录自启；不会覆盖任何已有 unit。"
	case "start":
		description = "启动已注册的应用插座服务。"
	case "stop":
		description = "停止已注册服务；不取消登录自启。"
	case "restart":
		description = "重启已注册服务，让监听参数或二进制变更生效。"
	case "reload":
		description = "向已注册的运行中服务发送 SIGHUP；重载失败时保留上一份有效策略。"
	case "status":
		description = "显示 systemd 中该 unit 的状态和近期日志。"
	case "uninstall":
		description = "停止服务、取消登录自启并移除已识别的用户 unit 文件或注册链接；不删除链接目标、配置、状态或二进制。"
	default:
		return fmt.Errorf("未知服务命令 %q，请查看 service --help", command)
	}
	fs := cliFlags("service "+command+" [参数]", description)
	unit := fs.String("unit", serviceDefaultUnit, "用户级 unit 文件名，必须匹配 app-plug*.service，例如 app-plug-proof.service")
	var config, stateDir, listen string
	var now bool
	if command == "install" {
		fs.StringVar(&config, "config", "", "策略 JSON 文件，默认 ~/.apps/app-plug/config.json")
		fs.StringVar(&stateDir, "state-dir", "", "已存在的路由状态目录，默认 ~/.apps/app-plug/state")
		fs.StringVar(&listen, "listen", "", "覆盖监听地址，格式 IP:端口；不修改策略")
		fs.BoolVar(&now, "now", false, "安装后立即启动；省略或使用 --now=false 只启用登录自启")
	}
	if err := parseCLI(fs, args[1:]); err != nil {
		return err
	}
	if len(*unit) > 255 || !serviceUnitName.MatchString(*unit) {
		return fmt.Errorf("--unit 必须是 app-plug*.service 范围内的普通 unit 名：%q", *unit)
	}
	if command == "status" {
		return runServiceSystemctl("status", "--no-pager", "--", *unit)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("读取用户主目录：%w", err)
	}
	configHome, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("读取用户配置目录：%w", err)
	}
	unitPath, err := filepath.Abs(filepath.Join(configHome, "systemd", "user", *unit))
	if err != nil {
		return err
	}
	if command == "install" {
		root := filepath.Join(home, ".apps", "app-plug")
		if config == "" {
			config = filepath.Join(root, "config.json")
		}
		if stateDir == "" {
			stateDir = filepath.Join(root, "state")
		}
		return installServiceUnit(unitPath, config, stateDir, listen, now)
	}

	entry, fragment, err := recognizedServiceUnit(unitPath, home)
	if err != nil {
		return err
	}
	if err := checkLoadedServiceUnit(*unit, fragment); err != nil {
		return err
	}
	if command != "uninstall" {
		return runServiceSystemctl(command, "--", *unit)
	}
	if err := checkServiceEnableLink(unitPath, fragment); err != nil {
		return err
	}
	// 先停止再取消自启；systemctl disable 在某些版本也会删除指向外部文件的注册链接。
	if err := runServiceSystemctl("stop", "--", *unit); err != nil {
		return err
	}
	if err := runServiceSystemctl("disable", "--no-reload", "--", *unit); err != nil {
		return err
	}
	if err := removeServiceEntry(unitPath, entry); err != nil {
		return err
	}
	return runServiceSystemctl("daemon-reload")
}

func checkLoadedServiceUnit(unit, fragment string) error {
	properties, err := serviceUnitProperties(unit)
	if err != nil {
		return err
	}
	loadedPath, err := filepath.EvalSymlinks(properties["FragmentPath"])
	if err != nil || loadedPath != fragment || properties["Id"] != unit || properties["LoadState"] != "loaded" {
		return fmt.Errorf("systemd 加载的 %s 不是已识别的用户 unit %q，拒绝操作", unit, fragment)
	}
	if properties["NeedDaemonReload"] != "no" || properties["DropInPaths"] != "" {
		return fmt.Errorf("%s 的 unit 文件已变更或有 drop-in，拒绝操作；请先检查 systemctl --user cat %s", unit, unit)
	}
	return nil
}

func installServiceUnit(unitPath, config, stateDir, listen string, now bool) error {
	unit := filepath.Base(unitPath)
	if _, err := os.Lstat(unitPath); err == nil {
		return fmt.Errorf("用户 unit %q 已存在，拒绝覆盖", unitPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查用户 unit：%w", err)
	}
	if err := checkServiceEnableLink(unitPath, unitPath); err != nil {
		return err
	}

	config, err := filepath.Abs(config)
	if err != nil {
		return fmt.Errorf("解析配置路径：%w", err)
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("解析状态目录：%w", err)
	}
	_, canonicalListen, err := server.CheckConfig(config, stateDir, listen)
	if err != nil {
		return err
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("读取当前二进制路径：%w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("解析当前二进制路径：%w", err)
	}
	// systemd 会检查解引号后的可执行文件名，拒绝控制字符、引号、反斜杠等字符。
	if !utf8.ValidString(executable) || strings.ContainsAny(executable, "\"'\\*?[") || strings.IndexFunc(executable, func(r rune) bool { return r < ' ' || r == 127 }) >= 0 {
		return fmt.Errorf("systemd 不支持当前二进制路径 %q，请移至普通路径后再安装", executable)
	}
	if info, err := os.Stat(executable); err != nil {
		return fmt.Errorf("读取当前二进制：%w", err)
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("当前二进制 %q 必须是可执行的普通文件", executable)
	}

	// 冒号禁止展开 $VAR，包括参数与二进制路径里的字面美元符号。
	start := ":" + quoteServiceArgument(executable) + " -config " + quoteServiceArgument(config) + " -state-dir " + quoteServiceArgument(stateDir)
	if listen != "" {
		start += " -listen " + quoteServiceArgument(canonicalListen)
	}
	contents := serviceUnitMarker + serviceUnitHead + "Type=exec\nExecStart=" + start + serviceUnitTail
	properties, err := serviceUnitProperties(unit)
	if err != nil {
		return err
	}
	if properties["LoadState"] != "not-found" || properties["ActiveState"] != "inactive" || properties["FragmentPath"] != "" || properties["DropInPaths"] != "" {
		return fmt.Errorf("systemd 中的 %s 已存在或有 drop-in，拒绝覆盖", unit)
	}
	// manager 可能缓存 not-found；从磁盘再检查其它搜索路径中是否已有同名 unit。
	out, err := serviceSystemctlOutput("list-unit-files", "--no-legend", "--no-pager", "--", unit)
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(exit.Stderr) != 0 || len(out) != 0 {
			return err
		}
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		return fmt.Errorf("systemd 搜索路径中已有 %s，拒绝覆盖", unit)
	}
	entry, err := writeServiceUnit(unitPath, contents)
	if err != nil {
		return fmt.Errorf("创建用户 unit %q：%w", unitPath, err)
	}
	if err := runServiceSystemctl("daemon-reload"); err != nil {
		return finishFailedServiceInstall(unitPath, entry, err)
	}
	written, err := os.Lstat(unitPath)
	if err == nil && !os.SameFile(entry, written) {
		err = fmt.Errorf("用户 unit %q 已被替换，拒绝启用", unitPath)
	}
	var fragment string
	if err == nil {
		fragment, err = filepath.EvalSymlinks(unitPath)
	}
	if err == nil {
		err = checkLoadedServiceUnit(unit, fragment)
	}
	if err != nil {
		return finishFailedServiceInstall(unitPath, entry, err)
	}
	if err := runServiceSystemctl("enable", "--", unit); err != nil {
		return finishFailedServiceInstall(unitPath, entry, err)
	}
	if now {
		if err := runServiceSystemctl("start", "--", unit); err != nil {
			return fmt.Errorf("%s 已启用登录自启，但立即启动失败：%w", unit, err)
		}
	}
	return nil
}

func finishFailedServiceInstall(path string, entry os.FileInfo, installErr error) error {
	// enable 可能已创建链接，只是 manager 重载失败。此时保留 unit，避免留下悬空的自启链接。
	if _, err := os.Lstat(serviceEnableLinkPath(path)); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("启用服务未完成，保留用户 unit %q：%w", path, installErr)
	}
	if err := removeServiceEntry(path, entry); err != nil {
		return errors.Join(installErr, err)
	}
	return errors.Join(installErr, runServiceSystemctl("daemon-reload"))
}

func quoteServiceArgument(arg string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(arg) + 2)
	b.WriteByte('"')
	for i := range len(arg) {
		c := arg[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '%':
			b.WriteString("%%")
		case c < ' ' || c >= 127:
			b.WriteString(`\x`)
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func writeServiceUnit(path, contents string) (os.FileInfo, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".app-plug-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(contents)
	info, statErr := f.Stat()
	if err := errors.Join(writeErr, statErr, f.Close()); err != nil {
		return nil, err
	}
	// 链接已写好的同目录临时文件；即使另一个进程抢先创建了同名文件也不会覆盖。
	if err := os.Link(f.Name(), path); err != nil {
		return nil, err
	}
	return info, nil
}

func recognizedServiceUnit(path, home string) (os.FileInfo, string, error) {
	entry, err := os.Lstat(path)
	if err != nil {
		return nil, "", fmt.Errorf("读取用户 unit：%w", err)
	}
	fragment, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", fmt.Errorf("无法解析用户 unit %q，拒绝操作：%w", path, err)
	}
	info, err := os.Stat(fragment)
	if err != nil {
		return nil, "", fmt.Errorf("读取用户 unit 文件：%w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 || filepath.Base(fragment) != filepath.Base(path) {
		return nil, "", fmt.Errorf("用户 unit %q 不是已知的 app-plug 服务文件，拒绝操作", path)
	}
	contents, err := os.ReadFile(fragment)
	if err != nil {
		return nil, "", fmt.Errorf("读取用户 unit 内容：%w", err)
	}
	if !isServiceUnitContents(string(contents), home) {
		return nil, "", fmt.Errorf("用户 unit %q 不是已知的 app-plug 服务文件，拒绝操作", path)
	}
	return entry, fragment, nil
}

func isServiceUnitContents(contents, home string) bool {
	body, generated := strings.CutPrefix(contents, serviceUnitMarker+serviceUnitHead+"Type=exec\nExecStart=")
	if generated {
		start, ok := strings.CutSuffix(body, serviceUnitTail)
		return ok && serviceGeneratedExec.MatchString(start)
	}
	// 同时识别仓库示例和安装目录中的手写 unit。
	body, ok := strings.CutPrefix(contents, serviceUnitHead+"Type=simple\n")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, serviceUnitTail)
	if !ok {
		return false
	}
	for _, prefix := range []string{"%h", strings.ReplaceAll(home, "%", "%%")} {
		nav := prefix + "/.apps/app-plug"
		legacy := strings.TrimPrefix(body, "WorkingDirectory="+nav+"\n")
		if oldServiceExecMatches(legacy, nav+"/app-plug", nav+"/config.json", nav+"/state") ||
			oldServiceExecMatches(body, prefix+"/.local/bin/app-plug", prefix+"/.config/app-plug/config.json", prefix+"/.local/state/app-plug") {
			return true
		}
	}
	return false
}

func oldServiceExecMatches(start, executable, config, state string) bool {
	rest, ok := strings.CutPrefix(start, "ExecStart="+executable+" -config "+config+" -state-dir "+state)
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	listen, ok := strings.CutPrefix(rest, " -listen ")
	if !ok {
		return false
	}
	addr, err := netip.ParseAddrPort(listen)
	return err == nil && addr.Port() != 0 && addr.Addr().Zone() == ""
}

func checkServiceEnableLink(path, fragment string) error {
	link := serviceEnableLinkPath(path)
	info, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查登录自启链接：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if resolved, err := filepath.EvalSymlinks(link); err == nil && resolved == fragment {
			return nil
		}
	}
	return fmt.Errorf("登录自启路径 %q 与该 unit 冲突，拒绝操作", link)
}

func serviceEnableLinkPath(path string) string {
	return filepath.Join(filepath.Dir(path), "default.target.wants", filepath.Base(path))
}

func removeServiceEntry(path string, entry os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查待移除的用户 unit：%w", err)
	}
	if !os.SameFile(entry, current) {
		return fmt.Errorf("用户 unit %q 已被替换，拒绝移除", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("移除用户 unit：%w", err)
	}
	return nil
}

func serviceUnitProperties(unit string) (map[string]string, error) {
	out, err := serviceSystemctlOutput("show", "--no-pager", "--property=Id,LoadState,ActiveState,FragmentPath,DropInPaths,NeedDaemonReload", "--", unit)
	props := make(map[string]string, 6)
	for line := range strings.SplitSeq(string(out), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			props[key] = value
		}
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || props["LoadState"] != "not-found" {
			return nil, err
		}
	}
	return props, nil
}

func runServiceSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl --user %s：%w", args[0], err)
	}
	return nil
}

func serviceSystemctlOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if detail := strings.TrimSpace(string(exit.Stderr)); detail != "" {
			return out, fmt.Errorf("systemctl --user %s：%w：%s", args[0], err, detail)
		}
	}
	return out, fmt.Errorf("systemctl --user %s：%w", args[0], err)
}
