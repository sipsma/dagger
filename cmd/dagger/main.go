package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"

	"go.dagger.io/dagger/cmd/dagger/cmd"
)

func init() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()
}

func main() {
	cmd.Execute()
}
