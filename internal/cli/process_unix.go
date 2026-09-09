//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func stopProcess(process *os.Process) error { return process.Signal(syscall.SIGTERM) }

func processRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer process.Release()
	return process.Signal(syscall.Signal(0)) == nil
}
