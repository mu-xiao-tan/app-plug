//go:build windows || darwin

package cli

import (
	"strings"
	"testing"
)

func TestServiceSubcommandsReportSystemdIsLinuxOnly(t *testing.T) {
	for _, command := range []string{"install", "start", "stop", "restart", "reload", "status", "uninstall"} {
		err := runServiceCLI([]string{command})
		if err == nil || !strings.Contains(err.Error(), "systemd") || !strings.Contains(err.Error(), "前台运行") {
			t.Errorf("%s: expected systemd capability error and foreground guidance, got %v", command, err)
		}
	}
}
