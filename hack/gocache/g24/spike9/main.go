// Spike 9 uses immutable Dagger results as the backing store.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fatal(fmt.Errorf("usage: spike9 helper|prepare|series|analyze"))
	}
	var err error
	switch os.Args[1] {
	case "fixture":
		err = fixtureMain(os.Args[2:])
	case "helper":
		err = helperMain(os.Args[2:])
	case "prepare":
		err = prepareMain(os.Args[2:])
	case "series":
		err = seriesMain(os.Args[2:])
	case "analyze":
		err = analyzeMain(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "spike9:", err)
	os.Exit(1)
}
