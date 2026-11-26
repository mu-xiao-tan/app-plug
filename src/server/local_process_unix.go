//go:build linux || darwin

package server

import (
	"fmt"
	"log"
	"os"
	"syscall"
	"time"
)

func validatePlatformExecutable(file string, info os.FileInfo) error {
	if info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("启动文件须为不带 setuid/setgid 的普通可执行文件或带 shebang 的脚本")
	}
	return nil
}

func (p *localProcess) terminateLocked(force bool) {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-p.pid, sig); err != nil && err != syscall.ESRCH {
		log.Printf("停止托管进程组 %d：%v", p.pid, err)
	}
}

func (p *localProcess) reapWithoutWaitid(process *os.Process) (stopping bool, err error) {
	// 本命令只使用文件描述符，无 os/exec 的 I/O 协程；Wait4 后只需释放进程句柄。
	defer process.Release()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		p.mu.Lock()
		var status syscall.WaitStatus
		var waited int
		for {
			waited, err = syscall.Wait4(process.Pid, &status, syscall.WNOHANG, nil)
			if err != syscall.EINTR {
				break
			}
		}
		if err != nil || waited != 0 {
			// 与 stop/join 共用锁，在收尸后、解锁前撤销 PID，防止组信号误伤复用的 PID。
			stopping = p.clearLocked()
			p.mu.Unlock()
			if err != nil {
				log.Printf("等待托管进程 %d 的 wait4 失败：%v", process.Pid, err)
				return stopping, err
			}
			if status.Signaled() {
				return stopping, fmt.Errorf("signal: %v", status.Signal())
			}
			if code := status.ExitStatus(); code != 0 {
				return stopping, fmt.Errorf("exit status %d", code)
			}
			return stopping, nil
		}
		p.mu.Unlock()
		<-tick.C
	}
}
