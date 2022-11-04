package docker

import (
	"syscall"

	exec "golang.org/x/sys/execabs"
)

func setPdeathsig(cmd *exec.Cmd) {
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
