package server

import (
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestLocalWaitFailureAllowsStopAndReaps(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-ec", "echo ready; read word")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, input, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	out, output, err := os.Pipe()
	if err != nil {
		in.Close()
		t.Fatal(err)
	}
	defer out.Close()
	cmd.Stdin, cmd.Stdout = in, output
	err = cmd.Start()
	in.Close()
	output.Close()
	if err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	p := &localProcess{pid: pid, done: make(chan struct{})}
	defer func() {
		p.stop()
		p.join()
	}()
	go func() {
		defer close(p.done)
		p.finishWait("test", cmd, syscall.EPERM)
	}()
	if err := out.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var reply [6]byte
	if _, err := io.ReadFull(out, reply[:]); err != nil || string(reply[:]) != "ready\n" {
		t.Fatalf("process did not respond after waitid failed: %q, %v", reply, err)
	}
	select {
	case <-p.done:
		t.Fatal("waitid failure killed or forgot a live child")
	case <-time.After(200 * time.Millisecond):
	}
	if !p.stop() {
		t.Fatal("failed waitid should not mark the application stopped")
	}
	p.join()
	if _, err := syscall.Wait4(pid, nil, syscall.WNOHANG, nil); err != syscall.ECHILD {
		t.Fatalf("process not reaped by managed stop: %v", err)
	}
}
