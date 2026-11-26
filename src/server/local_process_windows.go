package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/mu-xiao-tan/app-plug/src/internal/procnet"
)

var (
	terminateJob = syscall.NewLazyDLL("kernel32.dll").NewProc("TerminateJobObject")
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	createJob    = kernel32.NewProc("CreateJobObjectW")
	setJob       = kernel32.NewProc("SetInformationJobObject")
	assignJob    = kernel32.NewProc("AssignProcessToJobObject")
	queryJob     = kernel32.NewProc("QueryInformationJobObject")
	isInJob      = kernel32.NewProc("IsProcessInJob")
	threadFirst  = kernel32.NewProc("Thread32First")
	threadNext   = kernel32.NewProc("Thread32Next")
	openThread   = kernel32.NewProc("OpenThread")
	resumeThread = kernel32.NewProc("ResumeThread")
)

const (
	jobBasicProcessIDList = 3
	jobExtendedLimitInfo  = 9
	jobKillOnClose        = 0x2000
	createSuspended       = 0x00000004
)

type jobBasicLimits struct {
	perProcessTime, perJobTime     int64
	flags                          uint32
	minWorkingSet, maxWorkingSet   uintptr
	activeProcessLimit             uint32
	affinity                       uintptr
	priorityClass, schedulingClass uint32
}

type jobExtendedLimits struct {
	basic                                                      jobBasicLimits
	ioCounters                                                 [6]uint64
	processMemory, jobMemory, peakProcessMemory, peakJobMemory uintptr
}

type platformLocalProcess struct{ job syscall.Handle }

func validatePlatformExecutable(file string, _ os.FileInfo) error {
	// Start directly via CreateProcess; do not resolve script associations or PATHEXT.
	if !strings.EqualFold(filepath.Ext(file), ".exe") {
		return fmt.Errorf("Windows 启动文件须为普通 .exe 文件；请通过受信任的 .exe 及其参数运行脚本")
	}
	return nil
}

func (p *localProcess) start(cmd *exec.Cmd) (func(string), error) {
	h, _, callErr := createJob.Call(0, 0)
	if h == 0 {
		return nil, fmt.Errorf("CreateJobObjectW: %w", callErr)
	}
	job := syscall.Handle(h)
	limits := jobExtendedLimits{basic: jobBasicLimits{flags: jobKillOnClose}}
	if ok, _, err := setJob.Call(h, jobExtendedLimitInfo, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits)); ok == 0 {
		syscall.CloseHandle(job)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	// Assign the suspended child before it can spawn descendants that escape the job.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
	if err := cmd.Start(); err != nil {
		syscall.CloseHandle(job)
		return nil, err
	}
	var assignErr error
	err := cmd.Process.WithHandle(func(proc uintptr) {
		if ok, _, callErr := assignJob.Call(h, proc); ok == 0 {
			assignErr = fmt.Errorf("AssignProcessToJobObject: %w", callErr)
		}
	})
	if err = errors.Join(err, assignErr); err == nil {
		err = resumeInitialThread(uint32(cmd.Process.Pid))
	}
	if err != nil {
		_ = syscall.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	p.job = job
	return func(host string) {
		err := cmd.Wait()
		p.mu.Lock()
		p.terminateLocked(true)
		job := p.job
		p.job = 0
		stopping := p.clearLocked()
		p.mu.Unlock()
		waitJobEmpty(job)
		if !stopping {
			log.Printf("托管应用 %s 已退出：%v", host, err)
		}
	}, nil
}

// syscall.StartProcess closes the primary thread handle. A freshly created,
// suspended process has one thread; reopen it by its documented snapshot ID.
func resumeInitialThread(pid uint32) error {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer syscall.CloseHandle(snapshot)
	type threadEntry struct {
		size, usage, id, owner      uint32
		basePriority, deltaPriority int32
		flags                       uint32
	}
	var entry threadEntry
	first := true
	for {
		entry.size = uint32(unsafe.Sizeof(entry))
		api := threadNext
		if first {
			api, first = threadFirst, false
		}
		ok, _, err := api.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
		if ok == 0 {
			return fmt.Errorf("查找 Windows 初始线程：%w", err)
		}
		if entry.owner != pid {
			continue
		}
		const threadSuspendResume = 0x0002
		thread, _, err := openThread.Call(threadSuspendResume, 0, uintptr(entry.id))
		if thread == 0 {
			return fmt.Errorf("OpenThread: %w", err)
		}
		defer syscall.CloseHandle(syscall.Handle(thread))
		count, _, err := resumeThread.Call(thread)
		if uint32(count) == ^uint32(0) {
			return fmt.Errorf("ResumeThread: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("Windows 初始线程暂停计数异常：%d", count)
		}
		return nil
	}
}

func (p *localProcess) terminateLocked(_ bool) {
	// There is no SIGTERM equivalent for arbitrary Windows apps. Stop the
	// whole tree and keep the handle until descendants exit (port swaps).
	if p.job != 0 {
		if ok, _, err := terminateJob.Call(uintptr(p.job), 1); ok == 0 {
			log.Printf("停止 Windows 托管任务失败：%v", err)
			// Final-handle close is the kill-on-close fallback.
			if err := syscall.CloseHandle(p.job); err == nil {
				p.job = 0
			}
		}
	}
}

func waitJobEmpty(job syscall.Handle) {
	if job == 0 {
		return
	}
	defer syscall.CloseHandle(job)
	type accounting struct {
		times                                 [4]int64
		pageFaults, total, active, terminated uint32
	}
	const jobBasicAccounting = 1
	deadline := time.Now().Add(2 * time.Second)
	for {
		var info accounting
		ok, _, err := queryJob.Call(uintptr(job), jobBasicAccounting, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0)
		if ok == 0 {
			log.Printf("核实 Windows 托管任务已停止失败：%v", err)
			return
		}
		if info.active == 0 {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("等待 Windows 托管任务的剩余进程退出超时")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *localProcess) listeners(_ context.Context, port uint16) ([]netip.AddrPort, error) {
	const queryLimited = 0x1000
	// Keep live member handles until the TCP table has been read. The initial
	// listing isn't by itself proof against a recycled numeric PID.
	handles := make(map[int]syscall.Handle)
	defer func() {
		for _, h := range handles {
			syscall.CloseHandle(h)
		}
	}()
	for capacity := 16; ; capacity *= 2 {
		buf := make([]uintptr, 1+capacity) // two DWORDs followed by ULONG_PTR IDs on amd64/arm64.
		counts := (*[2]uint32)(unsafe.Pointer(&buf[0]))
		ok, _, err := queryJob.Call(uintptr(p.job), jobBasicProcessIDList, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))*unsafe.Sizeof(buf[0]), 0)
		if ok == 0 {
			if err == syscall.Errno(234) && capacity < 65536 { // ERROR_MORE_DATA
				continue
			}
			return nil, fmt.Errorf("QueryInformationJobObject: %w", err)
		}
		if counts[1] > uint32(capacity) || counts[0] != counts[1] {
			return nil, fmt.Errorf("Windows 返回了不完整的托管任务成员表")
		}
		for _, pid := range buf[1 : 1+int(counts[1])] {
			h, err := syscall.OpenProcess(queryLimited|syscall.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				continue
			}
			var member uint32
			ok, _, _ := isInJob.Call(uintptr(h), uintptr(p.job), uintptr(unsafe.Pointer(&member)))
			if ok != 0 && member != 0 {
				handles[int(pid)] = h
			} else {
				syscall.CloseHandle(h)
			}
		}
		break
	}
	return procnet.Listeners(func(pid int) bool {
		h, ok := handles[pid]
		if !ok {
			return false
		}
		state, err := syscall.WaitForSingleObject(h, 0)
		return err == nil && state == syscall.WAIT_TIMEOUT
	}, port)
}
