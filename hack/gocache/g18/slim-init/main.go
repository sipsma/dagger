//go:build linux

// Slim copy of cmd/init's mainInit (no session subprocess), to measure what the init's imports cost per process.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func main() {
	if err := mainInit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mainInit() error {
	sigCh := make(chan os.Signal, 16)
	signal.Notify(sigCh, syscall.SIGABRT, syscall.SIGALRM, syscall.SIGBUS, syscall.SIGCHLD, syscall.SIGCONT, syscall.SIGFPE, syscall.SIGHUP, syscall.SIGILL, syscall.SIGINT, syscall.SIGIO, syscall.SIGPIPE, syscall.SIGPROF, syscall.SIGPWR, syscall.SIGQUIT, syscall.SIGSEGV, syscall.SIGSTKFLT, syscall.SIGSYS, syscall.SIGTERM, syscall.SIGTRAP, syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGVTALRM, syscall.SIGWINCH, syscall.SIGXCPU, syscall.SIGXFSZ)
	_, err := unix.IoctlGetTermios(0, unix.TCGETS)
	_ = err
	fullPath := os.Args[1]
	if filepath.Base(fullPath) == fullPath {
		fullPath, err = exec.LookPath(fullPath)
		if errors.Is(err, exec.ErrDot) {
			err = nil
		}
		if err != nil {
			return err
		}
	}
	child, err := os.StartProcess(fullPath, os.Args[1:], &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}, Sys: &syscall.SysProcAttr{Setsid: true}})
	if err != nil {
		return err
	}
	for sig := range sigCh {
		if sig == syscall.SIGCHLD {
			for {
				var ws syscall.WaitStatus
				deadPid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
				if err != nil || deadPid == 0 {
					break
				}
				if deadPid == child.Pid {
					unix.Kill(-child.Pid, syscall.SIGTERM)
					os.Exit(ws.ExitStatus())
				}
			}
		}
	}
	return nil
}
