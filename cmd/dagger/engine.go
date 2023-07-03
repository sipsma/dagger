package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dagger/dagger/engine"
	internalengine "github.com/dagger/dagger/internal/engine"
	"github.com/dagger/dagger/internal/tui"
	"github.com/mattn/go-isatty"
	"github.com/vito/progrock"
	"github.com/vito/progrock/console"
)

var silent bool

var progress string
var stdoutIsTTY = isatty.IsTerminal(os.Stdout.Fd())
var stderrIsTTY = isatty.IsTerminal(os.Stderr.Fd())

var autoTTY = stdoutIsTTY || stderrIsTTY

func init() {
	rootCmd.PersistentFlags().BoolVarP(
		&silent,
		"silent",
		"s",
		false,
		"disable terminal UI and progress output",
	)

	rootCmd.PersistentFlags().StringVar(
		&progress,
		"progress",
		"auto",
		"progress output format (auto, plain, tty)",
	)
}

var interactive = os.Getenv("_EXPERIMENTAL_DAGGER_INTERACTIVE_TUI") != ""

type runSessionCallback func(context.Context, *engine.ClientSession) error

func withEngineAndTUI(
	ctx context.Context,
	sess *engine.ClientSession,
	fn runSessionCallback,
) error {
	// TODO: update for workdir being gone
	/*
		if engineConf.Workdir == "" {
			engineConf.Workdir = workdir
		}
	*/

	if sess.RunnerHost == "" {
		sess.RunnerHost = internalengine.RunnerHost()
	}

	sess.DisableHostRW = disableHostRW

	if sess.JournalFile == "" {
		sess.JournalFile = os.Getenv("_EXPERIMENTAL_DAGGER_JOURNAL")
	}

	if !silent {
		if progress == "auto" && autoTTY || progress == "tty" {
			if interactive {
				return interactiveTUI(ctx, sess, fn)
			}

			return inlineTUI(ctx, sess, fn)
		}

		sess.ProgrockWriter = console.NewWriter(os.Stderr, console.ShowInternal(debug))

		sess.EngineNameCallback = func(name string) {
			fmt.Fprintln(os.Stderr, "Connected to engine", name)
		}

		sess.CloudURLCallback = func(cloudURL string) {
			fmt.Fprintln(os.Stderr, "Dagger Cloud URL:", cloudURL)
		}
	}

	err := sess.Connect(ctx)
	if err != nil {
		return err
	}
	defer sess.Close()
	return fn(ctx, sess)
}

func progrockTee(progW progrock.Writer) (progrock.Writer, error) {
	if log := os.Getenv("_EXPERIMENTAL_DAGGER_PROGROCK_JOURNAL"); log != "" {
		fileW, err := newProgrockWriter(log)
		if err != nil {
			return nil, fmt.Errorf("open progrock log: %w", err)
		}

		return progrock.MultiWriter{progW, fileW}, nil
	}

	return progW, nil
}

func interactiveTUI(
	ctx context.Context,
	sess *engine.ClientSession,
	fn runSessionCallback,
) error {
	progR, progW := progrock.Pipe()
	progW, err := progrockTee(progW)
	if err != nil {
		return err
	}

	sess.ProgrockWriter = progW

	ctx, quit := context.WithCancel(ctx)
	defer quit()

	program := tea.NewProgram(tui.New(quit, progR), tea.WithAltScreen())

	tuiDone := make(chan error, 1)
	go func() {
		_, err := program.Run()
		tuiDone <- err
	}()

	err = sess.Connect(ctx)
	if err != nil {
		tuiErr := <-tuiDone
		return errors.Join(tuiErr, err)
	}
	defer sess.Close()

	err = fn(ctx, sess)
	tuiErr := <-tuiDone
	return errors.Join(tuiErr, err)
}

func inlineTUI(
	ctx context.Context,
	sess *engine.ClientSession,
	fn runSessionCallback,
) error {
	tape := progrock.NewTape()
	if debug {
		tape.ShowInternal(true)
	}

	progW, engineErr := progrockTee(tape)
	if engineErr != nil {
		return engineErr
	}

	sess.ProgrockWriter = progW

	ctx, quit := context.WithCancel(ctx)
	defer quit()

	program, stop := progrock.DefaultUI().RenderLoop(quit, tape, os.Stderr, true)
	defer stop()

	sess.CloudURLCallback = func(cloudURL string) {
		program.Send(progrock.StatusInfoMsg{
			Name:  "Cloud URL",
			Value: cloudURL,
			Order: 1,
		})
	}

	sess.EngineNameCallback = func(name string) {
		program.Send(progrock.StatusInfoMsg{
			Name:  "Engine",
			Value: name,
			Order: 2,
		})
	}

	err := sess.Connect(ctx)
	if err != nil {
		return err
	}
	defer sess.Close()
	return fn(ctx, sess)
}

func newProgrockWriter(dest string) (progrock.Writer, error) {
	f, err := os.Create(dest)
	if err != nil {
		return nil, err
	}

	return progrockFileWriter{
		enc: json.NewEncoder(f),
		c:   f,
	}, nil
}

type progrockFileWriter struct {
	enc *json.Encoder
	c   io.Closer
}

var _ progrock.Writer = progrockFileWriter{}

func (p progrockFileWriter) WriteStatus(update *progrock.StatusUpdate) error {
	return p.enc.Encode(update)
}

func (p progrockFileWriter) Close() error {
	return p.c.Close()
}
