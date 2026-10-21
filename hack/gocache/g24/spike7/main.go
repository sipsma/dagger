// Spike 7 is an experimental local model, not a production cache service.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fatal(fmt.Errorf("usage: spike7 helper|prepare|series|analyze"))
	}
	var err error
	switch os.Args[1] {
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
	fmt.Fprintln(os.Stderr, "spike7:", err)
	os.Exit(1)
}
