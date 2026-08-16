package fallback

import (
	"os/exec"
	"syscall"
)

// IDLE_PRIORITY_CLASS — вінда-еквівалент nice 19 для фонового транскоду.
const idlePriorityClass = 0x00000040

// lowPrioCmd — клас пріоритету задається прямо в CreateProcess.
func lowPrioCmd(args []string) *exec.Cmd {
	cmd := exec.Command(args[0], args[1:]...)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= idlePriorityClass
	return cmd
}

func applyLowPrio(int) {}
