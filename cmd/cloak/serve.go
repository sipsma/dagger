//go:build !linux
// +build !linux

package main

import (
	"github.com/spf13/cobra"
)

var serveCmd *cobra.Command
