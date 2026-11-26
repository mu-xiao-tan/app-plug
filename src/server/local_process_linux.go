package server

import (
	"context"
	"log"
	"net/netip"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/mu-xiao-tan/app-plug/src/internal/procnet"
)

type platformLocalProcess struct{}

func (p *localProcess) start(cmd *exec.Cmd) (func(string), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func(host string) { p.finishWait(host, cmd, waitLocalExit(cmd.Process.Pid)) }, nil
}

func (p *localProcess) listeners(_ context.Context, port uint16) ([]netip.AddrPort, error) {
	return procnet.Listeners(p.pid, port)
}

func (p *localProcess) finishWait(host string, cmd *exec.Cmd, errno syscall.Errno) {
	pid := cmd.Process.Pid
	var stopping bool
	var err error
	switch errno {
	case 0:
		p.mu.Lock()
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		stopping = p.clearLocked()
		p.mu.Unlock()
		err = cmd.Wait()
		// 安全撤销 PID 后，若 Go 自己的 waitid 不可用，仍由 Wait4 收尸。
		if cmd.ProcessState == nil {
			log.Printf("托管应用 %s 的 Wait 失败：%v", host, err)
			_, err = p.reapWithoutWaitid(cmd.Process)
		}
	case syscall.ECHILD:
		// 已无可等待的子进程，不再使用其数字 PID。
		p.mu.Lock()
		stopping = p.clearLocked()
		p.mu.Unlock()
		_ = cmd.Process.Release()
		err = errno
	default:
		log.Printf("托管应用 %s 的 waitid 失败：%v", host, errno)
		stopping, err = p.reapWithoutWaitid(cmd.Process)
	}
	if !stopping {
		log.Printf("托管应用 %s 已退出：%v", host, err)
	}
}

func waitLocalExit(pid int) syscall.Errno {
	// WNOWAIT 保留组长 PID，直到停止整个进程组，避免误操作已复用的 PID。
	const waitPID = 1 // Linux waitid 的 P_PID。
	var siginfo [16]uint64
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, waitPID, uintptr(pid), uintptr(unsafe.Pointer(&siginfo)), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno != syscall.EINTR {
			return errno
		}
	}
}
