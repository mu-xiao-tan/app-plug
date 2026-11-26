package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mu-xiao-tan/app-plug/src/server"
)

// PrintHelp 输出程序的顶层用法和命令组。
func PrintHelp(w io.Writer) {
	fmt.Fprintf(w, `应用插座 · App Plug %s
用法：
  app-plug [前台服务参数]
  app-plug app <命令> [参数]     通过部署 API 管理应用
  app-plug config <命令> [参数]  初始化、查看、校验与修改策略
  app-plug service <命令> [参数] 管理用户级 systemd 服务
  app-plug version             显示版本
  app-plug help [命令组]        显示帮助

使用「app-plug <命令组> --help」列出命令，使用
「app-plug <命令组> <命令> --help」查看每个参数。
`, server.Version)
}

// Run 分派去掉程序名之后的命令行参数。
func Run(args []string) error {
	if len(args) == 0 {
		PrintHelp(os.Stdout)
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	case "help":
		if len(rest) == 0 {
			PrintHelp(os.Stdout)
			return nil
		}
		if len(rest) > 2 || rest[0] == "help" {
			return errors.New("用法：app-plug help [命令组 [命令]]")
		}
		if len(rest) == 1 {
			return Run([]string{rest[0], "--help"})
		}
		return Run([]string{rest[0], rest[1], "--help"})
	case "version":
		fs := cliFlags("version", "显示当前程序版本。")
		if err := parseCLI(fs, rest); err != nil {
			return err
		}
		fmt.Println(server.Version)
		return nil
	case "app":
		return runAppCLI(rest)
	case "config":
		return runConfigCLI(rest)
	case "service":
		return runServiceCLI(rest)
	default:
		return fmt.Errorf("未知命令 %q，请查看 app-plug --help", command)
	}
}

func cliFlags(usage, description string) *flag.FlagSet {
	fs := flag.NewFlagSet(usage, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "用法：app-plug %s\n\n%s\n", usage, description)
		if hasCLIFlags(fs) {
			fmt.Fprintln(fs.Output(), "\n参数：")
			fs.PrintDefaults()
		}
	}
	return fs
}

func hasCLIFlags(fs *flag.FlagSet) bool {
	found := false
	fs.VisitAll(func(*flag.Flag) { found = true })
	return found
}

func parseCLI(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("不接受位置参数：%s；请查看该命令的 --help", strings.Join(fs.Args(), " "))
	}
	return nil
}

func cliGroupHelp(group, description string, commands ...string) error {
	fmt.Printf("用法：app-plug %s <命令> [参数]\n\n%s\n\n命令：\n", group, description)
	for _, command := range commands {
		fmt.Printf("  %s\n", command)
	}
	fmt.Printf("\n使用「app-plug %s <命令> --help」查看参数。\n", group)
	return nil
}
