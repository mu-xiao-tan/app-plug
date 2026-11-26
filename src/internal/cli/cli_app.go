package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mu-xiao-tan/app-plug/src/server"
)

func runAppCLI(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return cliGroupHelp("app", "通过部署 API 管理应用；需要已运行的实例及有权访问的令牌。",
			"list       列出有权管理的应用", "get        查看指定应用", "add        添加应用，不覆盖现有路由", "update     修改现有应用，只改变显式指定的参数",
			"delete     删除指定路由，须加 --yes", "start      启动本机托管应用", "stop       停止托管应用，保留路由", "claim      用临时 API Key 添加预填应用")
	}
	command := args[0]
	descriptions := map[string]string{
		"list": "列出当前令牌可管理的应用，输出 JSON。", "get": "查询一个应用的配置和运行状态，输出 JSON。",
		"add":    "添加一个应用。已有服务填 --address；本机托管填 --start-file，可加参数、端口和自启。",
		"update": "先读取配置，再保留未传入的字段。指定另一种接入方式会清除原方式专属字段。参数 --arg 会整体替换启动参数。",
		"delete": "移除路由。托管进程会停止，已有服务不受影响。需要 --yes。", "start": "启动本机托管应用，不改变自启开关。", "stop": "停止本机托管应用，配置仍保留。",
		"claim": "用管理员签发的临时 API Key 添加预填应用；只需回执中的域名。成功后密钥失效。",
	}
	description, ok := descriptions[command]
	if !ok {
		return fmt.Errorf("未知应用命令 %q，请查看 app --help", command)
	}
	fs := cliFlags("app "+command+" [参数]", description)
	origin := fs.String("url", "http://127.0.0.1:49101", "管理实例的 HTTP(S) 源，不带路径；远程使用可信 HTTPS 或 SSH 隧道")
	apiPath := fs.String("api-path", server.DefaultConfigSettings().APIPath, "实际部署 API 前缀，如 /api 或 /deploy，不带尾斜杠")
	tokenFile := fs.String("token-file", "", "读取 Bearer 令牌的私有文件；省略则读取 APP_PLUG_TOKEN 环境变量")
	var input server.DeployInput
	var yes, clearArgs bool
	if command != "list" {
		fs.StringVar(&input.Domain, "domain", "", "必填：应用完整域名或短名；更新及改名时填原域名")
	}
	if command == "delete" {
		fs.BoolVar(&yes, "yes", false, "确认移除指定路由，并停止该托管进程")
	}
	if command == "add" || command == "update" {
		fs.StringVar(&input.Name, "name", "", "应用显示名称；已有服务必填，托管可使用域名")
		fs.StringVar(&input.Description, "description", "", "可选简介；更新时传空字符串清除")
		fs.StringVar(&input.Address, "address", "", "已有 HTTP 服务的私有或回环 IP:端口；IPv6 用 [::1]:8080")
		fs.StringVar(&input.StartFile, "start-file", "", "服务主机上的可执行文件绝对路径；仅管理员可设置")
		fs.Func("arg", "启动参数，逐个传递；可重复，用 --arg=--option 传以连字符开头的值", func(arg string) error { input.Args = append(input.Args, arg); return nil })
		fs.IntVar(&input.Port, "port", 0, "托管应用自行监听的端口（1–65535），0 表示自动探测；更新旧节点时是节点端口")
		fs.BoolVar(&input.AutoStart, "auto-start", false, "仅本机托管；随导航启动，更新时可用 --auto-start=false 关闭")
	}
	if command == "update" {
		fs.BoolVar(&clearArgs, "clear-args", false, "清空托管应用原有启动参数，不能与 --arg 同用")
		fs.StringVar(&input.NewDomain, "new-domain", "", "修改后的完整域名或短名；旧节点路由改名须同时指定新接入方式")
		fs.StringVar(&input.Node, "node", "", "仅用于更新旧节点路由：节点名称，省略则保留原节点")
	}
	if err := parseCLI(fs, args[1:]); err != nil {
		return err
	}
	if command != "list" && !server.ValidAppDomain(input.Domain) {
		return errors.New("--domain 必须是完整域名或单个短名")
	}
	if command == "delete" && !yes {
		return errors.New("应用未删除；请明确传入 --yes")
	}
	client, err := newAppCLIClient(*origin, *apiPath, *tokenFile)
	if err != nil {
		return err
	}
	member := "/" + input.Domain
	var out []byte
	switch command {
	case "list":
		out, err = client.request(http.MethodGet, "", nil)
	case "get":
		out, err = client.request(http.MethodGet, member, nil)
	case "delete":
		out, err = client.request(http.MethodDelete, member, nil)
	case "start", "stop":
		out, err = client.request(http.MethodPost, member+"/"+command, nil)
	case "claim":
		out, err = client.request(http.MethodPost, "", struct {
			Domain string `json:"domain"`
		}{Domain: input.Domain})
	case "add":
		if err := validateAppCLIInput(input); err != nil {
			return err
		}
		out, err = client.request(http.MethodPost, "", input)
	case "update":
		if input.NewDomain != "" && !server.ValidAppDomain(input.NewDomain) {
			return errors.New("--new-domain 必须是完整域名或单个短名")
		}
		body, readErr := client.request(http.MethodGet, member, nil)
		if readErr != nil {
			return readErr
		}
		var old server.RouteResponse
		if err := json.Unmarshal(body, &old); err != nil {
			return fmt.Errorf("应用查询返回格式无效：%w", err)
		}
		merged, alias, mergeErr := mergeAppCLIInput(fs, old, input, clearArgs)
		if mergeErr != nil {
			return mergeErr
		}
		if alias != "" {
			member = "/" + alias
		}
		out, err = client.request(http.MethodPut, member, merged)
	}
	if err != nil {
		return err
	}
	if len(out) != 0 {
		if _, err := os.Stdout.Write(out); err != nil {
			return err
		}
		if out[len(out)-1] != '\n' {
			_, err = fmt.Fprintln(os.Stdout)
		}
	}
	return err
}

func validateAppCLIInput(in server.DeployInput) error {
	if (in.Address == "") == (in.StartFile == "") {
		return errors.New("--address 和 --start-file 必须且只能指定一个")
	}
	if in.StartFile == "" && (len(in.Args) != 0 || in.Port != 0 || in.AutoStart) {
		return errors.New("启动参数、端口和自启仅用于本机托管")
	}
	if in.Port < 0 || in.Port > 65535 {
		return errors.New("--port 必须为 0–65535")
	}
	return nil
}

func mergeAppCLIInput(fs *flag.FlagSet, old server.RouteResponse, change server.DeployInput, clearArgs bool) (server.DeployInput, string, error) {
	var result server.DeployInput
	if old.FileManaged {
		return result, "", errors.New("配置文件声明的 localApps 路由不可通过 REST 修改")
	}
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["name"] && !set["description"] && !set["address"] && !set["start-file"] && !set["arg"] && !clearArgs && !set["port"] && !set["auto-start"] && !set["node"] && !set["new-domain"] {
		return result, "", errors.New("至少指定一个需要修改的字段")
	}
	if clearArgs && set["arg"] {
		return result, "", errors.New("--clear-args 与 --arg 不可混用")
	}
	if set["address"] && set["start-file"] {
		return result, "", errors.New("不能同时更改为已有服务和本机托管")
	}
	if set["address"] && change.Address == "" || set["start-file"] && change.StartFile == "" || set["new-domain"] && change.NewDomain == "" {
		return result, "", errors.New("--address、--start-file、--new-domain 如指定则不能为空")
	}
	result.Name, result.Description = old.Name, old.Description
	if set["name"] {
		result.Name = change.Name
	}
	if set["description"] {
		result.Description = change.Description
	}
	if old.Alias != "" && !set["address"] && !set["start-file"] {
		if set["new-domain"] || set["arg"] || clearArgs || set["auto-start"] {
			return result, "", errors.New("旧节点路由改名或托管须同时指定 --address 或 --start-file")
		}
		result.Node, result.Port = old.Node, old.Port
		if set["node"] {
			result.Node = change.Node
		}
		if set["port"] {
			result.Port = change.Port
		}
		if result.Node == "" || result.Port < 1 || result.Port > 65535 {
			return result, "", errors.New("旧节点路由需要节点及 1–65535 端口")
		}
		return result, old.Alias, nil
	}
	if set["node"] {
		return result, "", errors.New("--node 仅用于保持旧节点接入方式的更新")
	}
	result.Domain, result.NewDomain = change.Domain, change.NewDomain
	result.Address, result.StartFile, result.Args, result.Port = old.Address, old.StartFile, old.Args, old.Port
	result.AutoStart = old.AutoStart != nil && *old.AutoStart
	if set["address"] {
		result.Address, result.StartFile, result.Args, result.Port, result.AutoStart = change.Address, "", nil, 0, false
	}
	if set["start-file"] {
		result.StartFile, result.Address = change.StartFile, ""
		if old.StartFile == "" {
			result.Port = 0
		}
	}
	if set["arg"] || clearArgs {
		result.Args = change.Args
	}
	if set["port"] {
		result.Port = change.Port
	}
	if set["auto-start"] {
		result.AutoStart = change.AutoStart
	}
	return result, "", validateAppCLIInput(result)
}

type appCLIClient struct{ endpoint, token string }

func newAppCLIClient(origin, apiPath, tokenFile string) (appCLIClient, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return appCLIClient{}, errors.New("--url 必须是无路径和凭据的 HTTP(S) 源")
	}
	if !server.ValidAPIPath(apiPath) {
		return appCLIClient{}, errors.New("--api-path 必须是 / 加单段小写别名")
	}
	token := os.Getenv("APP_PLUG_TOKEN")
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return appCLIClient{}, fmt.Errorf("读取令牌：%w", err)
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if !server.ValidManualToken(token) {
		return appCLIClient{}, errors.New("请通过 --token-file 或 APP_PLUG_TOKEN 提供有效的 Bearer 令牌")
	}
	return appCLIClient{endpoint: u.Scheme + "://" + u.Host + apiPath + server.RouteEndpoint, token: token}, nil
}

func (c appCLIClient) request(method, path string, value any) ([]byte, error) {
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	r, err := http.NewRequest(method, c.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Accept", "application/json")
	if value != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPut {
		r.Header.Set("If-Match", "*")
	}
	client := http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(r)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, server.MaxDeployResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > server.MaxDeployResponseBytes {
		return nil, errors.New("部署 API 响应过大")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Error == "" {
			problem.Error = http.StatusText(response.StatusCode)
		}
		return nil, fmt.Errorf("部署 API 返回 HTTP %d：%s", response.StatusCode, strings.ReplaceAll(problem.Error, c.token, "[已隐藏]"))
	}
	if response.StatusCode != http.StatusNoContent && !json.Valid(data) {
		return nil, errors.New("部署 API 未返回有效 JSON，请检查管理地址")
	}
	return data, nil
}
