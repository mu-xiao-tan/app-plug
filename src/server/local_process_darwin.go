package server

import (
	"context"
	"log"
	"net/netip"
	"os/exec"
	"syscall"
	"time"

	"github.com/mu-xiao-tan/app-plug/src/internal/procnet"
)

type platformLocalProcess struct{}

func (p *localProcess) start(cmd *exec.Cmd) (func(string), error) {
	syscall.ForkLock.RLock()
	kq, err := syscall.Kqueue()
	if err == nil {
		syscall.CloseOnExec(kq)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		syscall.Close(kq)
		return nil, err
	}
	return func(host string) { p.waitDarwin(host, cmd, kq) }, nil
}

func (p *localProcess) waitDarwin(host string, cmd *exec.Cmd, kq int) {
	defer syscall.Close(kq)
	var event [1]syscall.Kevent_t
	syscall.SetKevent(&event[0], cmd.Process.Pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	event[0].Fflags = syscall.NOTE_EXIT
	_, err := syscall.Kevent(kq, event[:], nil, &syscall.Timespec{})
	if err == nil {
		for {
			_, err = syscall.Kevent(kq, nil, event[:], nil)
			if err != syscall.EINTR {
				break
			}
		}
		if err == nil && event[0].Flags&syscall.EV_ERROR != 0 {
			err = syscall.Errno(event[0].Data)
		}
	} else if err == syscall.ESRCH {
		// Fast exit before registration: the leader has not been reaped yet.
		err = nil
	}
	var stopping bool
	if err != nil {
		log.Printf("托管应用 %s 的 kqueue 等待失败：%v", host, err)
		stopping, err = p.reapWithoutWaitid(cmd.Process)
	} else {
		// Clear only after signalling the group; Wait can recycle its PID.
		p.mu.Lock()
		p.terminateLocked(true)
		stopping = p.clearLocked()
		p.mu.Unlock()
		err = cmd.Wait()
	}
	if !stopping {
		log.Printf("托管应用 %s 已退出：%v", host, err)
	}
}

func (p *localProcess) listeners(ctx context.Context, port uint16) ([]netip.AddrPort, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return procnet.Listeners(ctx, p.pid, port)
}
