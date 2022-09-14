package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	stdio "go.dagger.io/dagger/internal/stdio"
)

var dialStdioCmd = &cobra.Command{
	Use:    "dial-stdio",
	Run:    DialStdio,
	Hidden: true,
}

// Hack to connect to buildkit using stdio:
// https://github.com/moby/buildkit/blob/f567525314aa6b37970cad1c6f43bef449b71e04/client/connhelper/dockercontainer/dockercontainer.go#L32
func DialStdio(cmd *cobra.Command, args []string) {
	err := stdio.DialStdioAction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
