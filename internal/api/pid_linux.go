package api

import "syscall"

// pidAlive — чи живий процес; будь-яка помилка kill(pid, 0) = «ні».
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
