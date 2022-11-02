//go:build !linux
// +build !linux

package dockerexec

import (
	"os/exec"
)

func setPdeathsig(cmd *exec.Cmd) {
}
