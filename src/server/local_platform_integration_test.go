package server

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestManagedProcessFindsDescendantAndReleasesPort(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	port, _ := localTestPorts(t)
	changed := make(chan struct{}, 1)
	p, err := startLocal("test", localAppConfig{
		StartFile: exe, Port: 0,
		Args: []string{"-test.run=^TestLocalListenerParentProcess$", "--", strconv.Itoa(int(port))},
	}, 0, changed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.stop(); p.join() }()
	select {
	case <-changed:
	case <-p.done:
		t.Fatal("managed parent exited before its listener was discovered")
	case <-time.After(10 * time.Second):
		t.Fatal("listener belonging to a managed descendant was not discovered")
	}
	if got := p.target.Load(); got == nil || got.Port() != strconv.Itoa(int(port)) {
		t.Fatalf("wrong descendant address: %v", got)
	}
	if !p.stop() {
		t.Fatal("managed parent was not running")
	}
	p.join()
	if p.running() || p.target.Load() != nil {
		t.Fatal("process still running or routable after stop and join")
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	deadline := time.Now().Add(2 * time.Second)
	for {
		listener, err := net.Listen("tcp4", addr)
		if err == nil {
			listener.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed descendant still owns the listener: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLocalListenerParentProcess(t *testing.T) {
	if len(os.Args) != 4 || os.Args[1] != "-test.run=^TestLocalListenerParentProcess$" || os.Args[2] != "--" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestLocalListenerProcess$", "--", os.Args[3])
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedProcessRejectsUnrelatedListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := startLocal("test", localAppConfig{
		StartFile: exe, Port: port,
		Args: []string{"-test.run=^TestLocalListenerProcess$", "--", "0"},
	}, 0, make(chan struct{}, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.stop(); p.join() }()
	select {
	case <-p.done:
		t.Fatal("managed process exited before ownership check")
	case <-time.After(1200 * time.Millisecond):
	}
	if got := p.target.Load(); got != nil {
		t.Fatalf("routed to an unrelated listener: %v", got)
	}
}
