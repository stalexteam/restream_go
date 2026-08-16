package api

import "syscall"

const processQueryLimitedInformation = 0x1000

// pidAlive — Windows-еквівалент os.kill(pid, 0).
func pidAlive(pid int) bool {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	var code uint32
	err = syscall.GetExitCodeProcess(handle, &code)
	syscall.CloseHandle(handle)
	return err == nil && code == stillActive
}

const stillActive = 259
