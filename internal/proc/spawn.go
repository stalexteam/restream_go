package proc

import "os/exec"

// StartCmd — спавн під захистом від сиріт.
func StartCmd(cmd *exec.Cmd) error {
	ConfigureCmd(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	GuardStarted(cmd)
	return nil
}
