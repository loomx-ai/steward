package cli

import (
	"os"
	"syscall"
)

// Windows cannot send SIGTERM to another process. Ctrl+C in the server's
// terminal remains the graceful shutdown path.
func stopProcess(process *os.Process) error { return process.Kill() }

func processRunning(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	return syscall.GetExitCodeProcess(handle, &code) == nil && code == 259 // STILL_ACTIVE
}
