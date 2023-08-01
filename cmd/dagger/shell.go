package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"dagger.io/dagger"
	"github.com/containerd/console"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/engine/client"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func init() {
	rootCmd.AddCommand(shellCmd)
}

var shellCmd = &cobra.Command{
	Use:                "shell",
	DisableFlagParsing: true,
	Hidden:             true, // for now, remove once we're ready for primetime
	// TODO: RunE:               loadEnvCmdWrapper(RunShell),
	RunE: RunShell,
}

var (
	// TODO:dedupe w/ same thing in core
	stdinPrefix  = []byte{0, byte(',')}
	stdoutPrefix = []byte{1, byte(',')}
	stderrPrefix = []byte{2, byte(',')}
	resizePrefix = []byte("resize,")
	exitPrefix   = []byte("exit,")
)

/*
	TODO:

func RunShell(

	ctx context.Context,
	engineClient *client.Client,
	c *dagger.Client,
	env *dagger.Environment,
	_ *cobra.Command,
	_ []string,

) error {
*/
func RunShell(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	engineClient, ctx, err := client.Connect(ctx, client.Params{
		RunnerHost: engine.RunnerHost(),
	})
	if err != nil {
		return err
	}
	defer engineClient.Close()

	c, err := dagger.Connect(ctx, dagger.WithConn(EngineConn(engineClient)))
	if err != nil {
		return err
	}
	defer c.Close()

	// TODO:
	ctr := c.Container().From("alpine:3.18")

	shellEndpoint, err := ctr.ShellEndpoint(ctx)
	if err != nil {
		return err
	}
	// TODO:
	fmt.Fprintf(os.Stderr, "shell endpoint: %s\n", shellEndpoint)

	dialer := &websocket.Dialer{
		// TODO: need use DialNestedContext when, well, you know, nested. Fix in engine client
		NetDialContext: engineClient.DialContext,
		// TODO:
		// HandshakeTimeout: 60 * time.Second, // TODO: made up number
	}
	wsconn, _, err := dialer.DialContext(ctx, shellEndpoint, nil)
	if err != nil {
		return err
	}
	defer wsconn.Close()

	// TODO:
	fmt.Fprintf(os.Stderr, "WE ARE SO CONNECTED\n")
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	if err := wsconn.WriteMessage(websocket.BinaryMessage, []byte("hello")); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)

	// Handle terminal sizing
	current := console.Current()
	sendTermSize := func() error {
		var (
			width  = 80
			height = 120
		)
		size, err := current.Size()
		if err == nil {
			width, height = int(size.Width), int(size.Height)
		}
		message := append([]byte{}, resizePrefix...)
		message = append(message, []byte(fmt.Sprintf("%d;%d", width, height))...)
		return wsconn.WriteMessage(websocket.BinaryMessage, message)
	}
	// Send the current terminal size right away (initial sizing)
	err = sendTermSize()
	if err != nil {
		return fmt.Errorf("failed to send terminal size: %w", err)
	}
	// Send updates as terminal gets resized
	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)
	go func() {
		for range sigWinch {
			err := sendTermSize()
			if err != nil {
				// TODO:
				fmt.Fprintf(os.Stderr, "failed to send terminal size: %v\n", err)
			}
		}
	}()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set stdin to raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Handle incoming messages
	errCh := make(chan error)
	exitCode := 1
	go func() {
		defer close(errCh)

		for {
			_, buff, err := wsconn.ReadMessage()
			if err != nil {
				errCh <- fmt.Errorf("read: %w", err)
				return
			}
			switch {
			case bytes.HasPrefix(buff, stdoutPrefix):
				os.Stdout.Write(bytes.TrimPrefix(buff, stdoutPrefix))
			case bytes.HasPrefix(buff, stderrPrefix):
				os.Stderr.Write(bytes.TrimPrefix(buff, stderrPrefix))
			case bytes.HasPrefix(buff, exitPrefix):
				code, err := strconv.Atoi(string(bytes.TrimPrefix(buff, exitPrefix)))
				if err == nil {
					exitCode = code
				}
			}
		}
	}()

	// Forward stdin to websockets
	go func() {
		for {
			b := make([]byte, 512)

			n, err := os.Stdin.Read(b)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read: %v\n", err)
				continue
			}
			message := append([]byte{}, stdinPrefix...)
			message = append(message, b[:n]...)
			err = wsconn.WriteMessage(websocket.BinaryMessage, message)
			if err != nil {
				fmt.Fprintf(os.Stderr, "write: %v\n", err)
				continue
			}
		}
	}()

	if err := <-errCh; err != nil {
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			return fmt.Errorf("websocket close: %w", err)
		}
	}
	if exitCode != 0 {
		return fmt.Errorf("exited with code %d", exitCode)
	}
	return nil
}
