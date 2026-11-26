//go:build linux || darwin

package server

import (
	"log"
	"os"
	"path/filepath"
	"syscall"
)

func lockStateFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func openPrivateConfig(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
func privateConfigTemp(dir string) (*os.File, error) { return os.CreateTemp(dir, ".app-plug-*") }
func commitConfigFile(dir, from, to string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.Rename(from, to); err != nil {
		return err
	}
	// Rename is the commit point. A later sync failure cannot roll it back.
	if err := f.Sync(); err != nil {
		log.Printf("%s 已写入，但同步目录失败，断电持久性未确认：%v", filepath.Base(to), err)
	}
	return nil
}
