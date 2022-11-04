//go:build !linux
// +build !linux

package docker

import (
	"os/exec"
)

func setPdeathsig(cmd *exec.Cmd) {
}
