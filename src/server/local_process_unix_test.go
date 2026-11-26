//go:build linux || darwin

package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStartRechecksExecutableMode(t *testing.T) {
	s, p := newLocalTestStore(t)
	file := filepath.Join(s.dir, "app")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := p.resolveApp(appInput{Domain: "demo", StartFile: file}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0700 | os.ModeSetuid, 0700 | os.ModeSetgid} {
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		process, err := startLocal(a.host, *a.local, p.listener.Port(), s.changed)
		if err == nil {
			process.stop()
			process.join()
			t.Errorf("started executable with forbidden mode %v", mode)
		}
	}
}
