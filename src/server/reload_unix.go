//go:build linux || darwin

package server

import (
	"os"
	"os/signal"
	"syscall"
)

func configReloadSignal() (<-chan os.Signal, func()) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	return hup, func() { signal.Stop(hup) }
}
