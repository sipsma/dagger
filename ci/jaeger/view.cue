package main

import (
	"dagger.io/dagger"
	"dagger.io/dagger/core"

	"universe.dagger.io/alpine"
	"universe.dagger.io/docker"
)

dagger.#Plan & {
	// TODO: update to use ability to read from env var once that's merged
	client: filesystem: "~/.config/gh/hosts.yml": read: contents?: dagger.#Secret

  actions: {
		// TODO: document, mildly hacky but works and is improveable
		// TODO: move this to its own cue file
		jaegerView: {
			runID:        string
			repository:   string | *"dagger/dagger"
			artifactName: string | *"jaeger_data"

			_ghImage: alpine.#Build & {
				packages: "github-cli": _
			}
			_dl: docker.#Run & {
				input: _ghImage.output
				mounts: gh: {
					dest:     "/root/.config/gh/hosts.yml"
					contents: client.filesystem["~/.config/gh/hosts.yml"].read.contents
				}
				command: {
					name: "gh"
					args: [
						"run",
						"download",
						runID,
						"-n", artifactName,
						"-R", repository,
						"-D", "/output",
					]
				}
			}
			_jaegerData: core.#Subdir & {
				input: _dl.output.rootfs
				path:  "/output"
			}

			_jaegerImage: docker.#Pull & {
				source: "jaegertracing/all-in-one:1.33.0"
			}
			run: docker.#Run & {
				input: _jaegerImage.output
				mounts: data: {
					dest:     "/badger"
					contents: _jaegerData.output
				}
				env: {
					SPAN_STORAGE_TYPE:      "badger"
					BADGER_EPHEMERAL:       "false"
					BADGER_DIRECTORY_VALUE: "/badger/data"
					BADGER_DIRECTORY_KEY:   "/badger/key"
				}
			}
		}
  }
}

