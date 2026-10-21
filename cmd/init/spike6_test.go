//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/distconsts"
	"github.com/stretchr/testify/require"
)

func TestSpike6InitBarrier(t *testing.T) {
	for _, release := range []bool{true, false} {
		t.Run(map[bool]string{true: "release", false: "cancel"}[release], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			readyR, readyW, err := os.Pipe()
			require.NoError(t, err)
			defer readyR.Close()
			defer readyW.Close()
			releaseR, releaseW, err := os.Pipe()
			require.NoError(t, err)
			defer releaseR.Close()
			defer releaseW.Close()
			target := filepath.Join(t.TempDir(), "ran")
			cmd := exec.CommandContext(ctx, os.Args[0], "sh", "-c", `test -z "$`+distconsts.Spike6ReadyFDEnv+`" && test -z "$`+distconsts.Spike6ReleaseFDEnv+`" && test ! -e /proc/$$/fd/3 && test ! -e /proc/$$/fd/4 && echo ran > "$1"`, "sh", target)
			cmd.Env = initEnv(distconsts.Spike6ReadyFDEnv+"=3", distconsts.Spike6ReleaseFDEnv+"=4")
			cmd.ExtraFiles = []*os.File{readyW, releaseR}
			require.NoError(t, cmd.Start())
			readyW.Close()
			releaseR.Close()
			readyR.SetReadDeadline(time.Now().Add(10 * time.Second))
			var b [1]byte
			_, err = io.ReadFull(readyR, b[:])
			require.NoError(t, err)
			require.NoFileExists(t, target)
			if release {
				_, err = releaseW.Write([]byte{1})
				require.NoError(t, err)
			}
			releaseW.Close()
			err = cmd.Wait()
			if release {
				require.NoError(t, err)
				require.FileExists(t, target)
			} else {
				require.Error(t, err)
				require.NoFileExists(t, target)
			}
		})
	}
}
