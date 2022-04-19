package main

import (
	"dagger.io/dagger"
	"dagger.io/dagger/core"
)

dagger.#Plan & {
	actions: {
    baseImage: core.#Pull & {
      source: "alpine:3.15.0"
    }

    image: core.#Exec & {
      input: baseImage.output
      args: [
        "sh", "-c", "echo 18 && dd if=/dev/urandom bs=32 count=1 status=none | md5sum && sleep 1",
      ]
    }

    imageB: core.#Exec & {
      input: image.output
      args: [
        "sh", "-c", "echo 17 && dd if=/dev/urandom bs=32 count=1 status=none | md5sum && sleep 1",
      ]
    }

		// Push image with random content
		push: core.#Push & {
			dest:  "127.0.0.1:5000/testdupe:latest"
			input: imageB.output
		}
	}
}
