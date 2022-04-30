
package main

import (
	"dagger.io/dagger"
	"dagger.io/dagger/core"

	"universe.dagger.io/alpine"
	"universe.dagger.io/docker"
)

#Dagger: {
  namespace: string
	baseEnv: [key=string]: string
  daggerSrc: dagger.#FS

  _buildkitd: {
    _repo: core.#GitPull & {
      remote: "https://github.com/sipsma/buildkit.git"
      ref: "cache-test"
    }

    build: core.#Dockerfile & {
      source: _repo.output
    }

    run: core.#Start & {
      input: build.output
      args: [
        "sh", "-c", #"""
          buildkitd --debug >/var/log/buildkitd.log 2>&1
        """#
      ]
      env: {
        "JAEGER_TRACE": "localhost:6831"
        "OTEL_EXPORTER_JAEGER_ENDPOINT": "http://localhost:14268/api/traces"
      }
      privileged: true
      mounts: {
        "state": {
          dest: "/var/lib/buildkit"
          contents: dagger.#Scratch
        }
        "sock": {
          dest: "/run/buildkit"
          contents: core.#CacheDir & {
            id: "buildkitd-sock-\(namespace)"
          }
        }
        "log": {
          dest: "/var/log"
          contents: core.#CacheDir & {
            id: "buildkitd-log-\(namespace)"
          }
        }
      }
    }
  }

  client: {
    _image: alpine.#Build & {
      packages: bash: _
      packages: yarn: _
      packages: go: _
      packages: make: _
      packages: git: _
      packages: gcc: _
      packages: "libc-dev": _
      packages: parallel: _
      packages: jq: _
    }

    run: core.#Exec & {
      input: _image.output.rootfs
      args: [
        "sh", "-c", #"""
          yarn --cwd "./tests" install
          DAGGER_BINARY="/src/bin/dagger" yarn --cwd "./tests" test
          sleep 5
        """#
        // ./cmd/dagger/dagger do --log-format=plain -p tests/tasks/merge/merge.cue test
      ]
      env: {
        "BUILDKIT_HOST": "unix:///run/buildkit/buildkitd.sock"
        "JAEGER_TRACE": "localhost:6831"
        "OTEL_EXPORTER_JAEGER_ENDPOINT": "http://localhost:14268/api/traces"
      } & baseEnv
      workdir: "/src"
      mounts: {
        "src": {
          dest: "/src"
          contents: daggerSrc
        }
        "sock": {
          dest: "/run/buildkit"
          contents: core.#CacheDir & {
            id: "buildkitd-sock-\(namespace)"
          }
        }
      }
    } & {
      _dep: _buildkitd.run
    }
  }

  export: {
    _image: alpine.#Build

    run: core.#Exec & {
      input: _image.output.rootfs
      args: [
        "cp", "-r", "/log", "/output"
      ]
      mounts: {
        "log": {
          dest: "/log"
          contents: core.#CacheDir & {
            id: "buildkitd-log-\(namespace)"
          }
        }
      }
    } & {
      _dep: client.run
    }

    subdir: core.#Subdir & {
      input: run.output
      path:   "/output"
    }
  }
}

dagger.#Plan & {
	client: filesystem: ".": read: exclude: [
		"**/node_modules",
	]
	client: filesystem: "./jaeger": write: contents: actions.e2etest.export.subdir.output
	client: filesystem: "./logs/withCache": write: contents: actions.e2etest.withCache.export.subdir.output
	client: filesystem: "./logs/withoutCache": write: contents: actions.e2etest.withoutCache.export.subdir.output

  client: env: {
    "DAGGER_CACHE_TO"?: string
    "DAGGER_CACHE_FROM"?: string
    "ACTIONS_RUNTIME_TOKEN"?: string
    "ACTIONS_CACHE_URL"?: string
    "GITHUB_ACTIONS"?: string
  }

	actions: {
    _daggerSrc: client.filesystem["."].read.contents

    e2etest: {
      _jaeger: {
        image: docker.#Pull & {
          source: "jaegertracing/all-in-one:1.33.0"
        }

        run: core.#Start & {
          input: image.output.rootfs
          args: image.output.config.entrypoint
          env: {
            "SPAN_STORAGE_TYPE": "badger"
            "BADGER_EPHEMERAL": "false"
            "BADGER_DIRECTORY_VALUE": "/badger/data"
            "BADGER_DIRECTORY_KEY": "/badger/key"
          }
          mounts: {
            "state": {
              dest: "/badger"
              contents: core.#CacheDir & {
                id: "jaeger-state"
              }
            }
          }
        }
      }

      withCache: #Dagger & {
        namespace: "with-cache"
        daggerSrc: _daggerSrc
        baseEnv: {
          "DAGGER_CACHE_FROM": client.env.DAGGER_CACHE_FROM
          "DAGGER_CACHE_TO": client.env.DAGGER_CACHE_TO
          "ACTIONS_RUNTIME_TOKEN": client.env.ACTIONS_RUNTIME_TOKEN
          "ACTIONS_CACHE_URL": client.env.ACTIONS_CACHE_URL
          "GITHUB_ACTIONS": client.env.GITHUB_ACTIONS
          "DAGGER_TRACE_SPAN_NAME": "withCache"
        }
      }

      withoutCache: #Dagger & {
        namespace: "without-cache"
        daggerSrc: _daggerSrc
        baseEnv: {
          "DAGGER_TRACE_SPAN_NAME": "withoutCache"
        }
      }

      export: {
        _image: alpine.#Build

        stopJaeger: core.#Stop & {
          input: _jaeger.run
        } & {
          _dep: [withCache.client.run, withoutCache.client.run]
        }

        run: core.#Exec & {
          input: _image.output.rootfs
          // copy from /badger to /output
          args: [
            "cp", "-r", "/badger", "/output"
          ]
          mounts: {
            "state": {
              dest: "/badger"
              contents: core.#CacheDir & {
                id: "jaeger-state"
              }
            }
          }
        } & {
          _dep: stopJaeger
        }

        subdir: core.#Subdir & {
          input: run.output
          path:   "/output"
        }
      } 
    }
  }
}
