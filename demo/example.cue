package main

import (
	"dagger.io/dagger"
	"dagger.io/dagger/core"

	"universe.dagger.io/go"
)

dagger.#Plan & {
  actions: {
		_src: core.#GitPull & {
      remote: "https://github.com/org/client.git"
      ref: "main"
    }

    build: go.#Build & {
      source: _src.output
      package: "./client/"
    }

    test: go.#Test & {
			source:  _src.output
			package: "./client/"
			command: flags: {
        "-race": true
      }
    }
  }
}
