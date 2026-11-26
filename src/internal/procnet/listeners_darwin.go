package procnet

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"time"
)

// Discover via the system lsof. Enumerate all visible listeners (not just the
// child) so overlapping bindings can be rejected. lsof only sees sockets that
// the running user has permission to inspect.
func Listeners(ctx context.Context, pgid int, port uint16) ([]netip.AddrPort, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-iTCP", "-sTCP:LISTEN", "-F0pgtfn")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0 && len(exit.Stderr) == 0 {
			return nil, nil // lsof returns 1 when no rows matched.
		}
		return nil, fmt.Errorf("读取 macOS TCP 监听表（lsof）：%w", err)
	}
	rows, groups, err := parseLsofListeners(out)
	if err != nil {
		return nil, err
	}
	return selectOwnedListeners(rows, func(pid int) bool { return groups[pid] == pgid }, port), nil
}
