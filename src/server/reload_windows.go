package server

import "os"

// Windows 无 SIGHUP，返回 nil channel 禁用该 select 分支；策略变更可重启前台服务。
func configReloadSignal() (<-chan os.Signal, func()) { return nil, func() {} }
