package fallback

import (
	"os/exec"
	"syscall"
)

// lowPrioCmd — команда транскоду з nice 19 через `nice`: він виставляє
// пріоритет ДО exec, тож усі потоки ffmpeg успадковують його.
func lowPrioCmd(args []string) *exec.Cmd {
	argv := args
	if nice, err := exec.LookPath("nice"); err == nil {
		argv = append([]string{nice, "-n", "19"}, args...)
	}
	return exec.Command(argv[0], argv[1:]...)
}

// applyLowPrio — добірка після Start: закриває вікно між fork і setpriority
// усередині `nice` і рятує, коли самого `nice` у системі немає.
func applyLowPrio(pid int) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 19)
}
