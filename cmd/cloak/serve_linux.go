package main

import (
	"github.com/spf13/cobra"
	buildkitd "go.dagger.io/dagger/internal/buildkitd/bundling"
)

var serveCmd = &cobra.Command{
	Use: "serve",
	Run: Serve,
}

func Serve(cmd *cobra.Command, args []string) {
	buildkitd.Run()
}
