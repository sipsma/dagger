package main

import (
	"dagger.io/dagger"
	"dagger.io/dagger/core"

	"universe.dagger.io/alpine"
	"universe.dagger.io/aws"
	"universe.dagger.io/go"
	"universe.dagger.io/yarn"

	"universe.dagger.io/x/david@rawkode.dev/pulumi"
)

dagger.#Plan & {
  client: filesystem: ".": read: contents: dagger.#FS
  client: filesystem: "bin/": write: contents: actions.buildClient.output

  client: env: {
    AWS_ACCESS_KEY_ID: dagger.#Secret
    AWS_SECRET_ACCESS_KEY: dagger.#Secret
		PULUMI_ACCESS_TOKEN: dagger.#Secret
  }

  actions: {
		_source: client.filesystem["."].read.contents

    // Client build+test
    buildClient: go.#Build & {
      source: _source
      package: "./client/"
      tags: "netgo"
      os:      client.platform.os
      arch:    client.platform.arch
    }
    unitTestClient: go.#Test & {
			source:  _source
			package: "./client/"
			command: flags: {
        "-race": true
      }
    }

    // Server build+test
    _serverDir: core.#Subdir & {
      input: _source
      path: "./server"
    }
    buildServer: #Zip & {
      zipInput: _serverDir.output
      subpath: "src"
      zipName: "lambda.zip"
    }
    unitTestServer: yarn.#Run & {
			source:  _serverDir.output
			script: "test"
    }

    // Integ tests
    deployTestServer: {
      _up: pulumi.#Up & {
        deployServerOpts
        stack: "dev"
      }
      url: _up.outputs.url
    }
    integTest: #IntegTest & {
      expectedOutput: "Foo: Hello from Lambda!"
      clientBin: buildClient.output
      serverUrl: deployTestServer.url
    }

    // Release Client
    uploadClient: aws.#Container & {
      credentials: aws.#Credentials & {
        accessKeyId: client.env.AWS_ACCESS_KEY_ID
        secretAccessKey: client.env.AWS_SECRET_ACCESS_KEY
      }
      mounts: {
        "src": core.#Mount & {
          dest: "/src"
          contents: buildClient.output
          ro: true
        }
        "test": core.#Mount & {
          dest: "/test"
          contents: unitTestClient.output.rootfs
          ro: true
        }
        "integtest": core.#Mount & {
          dest: "/integtest"
          contents: integTest.output
          ro: true
        }
      }
      command: {
        name: "aws",
        args: ["s3", "cp", "/src/client", "s3://sipsma-dagger-demo/client"]
      }
    }

    // Deploy Server
    deployProdServer: {
      _up: pulumi.#Up & {
        deployServerOpts
        stack: "prod"
        container: mounts: {
          "test": core.#Mount & {
            dest: "/test"
            contents: unitTestServer.output
            ro: true
          }
          "integtest": core.#Mount & {
            dest: "/integtest"
            contents: integTest.output
            ro: true
          }
        }
      }
      url: _up.outputs.url
    }

    all: {
      // targets
      _clientBin: uploadClient
      _server: deployProdServer

      // outputs
      url: _server.url
    }

    // common opts
    _pulumiDir: core.#Subdir & {
      input: _source
      path: "./pulumi"
    }
    deployServerOpts: {
      _modCachePath:   "/root/.cache/go-mod"
      _buildCachePath: "/root/.cache/go-build"
      _pulumiPluginCachePath: "/root/.pulumi/plugins"

      stackCreate: true
      runtime: "go"
      accessToken: client.env.PULUMI_ACCESS_TOKEN
      source: _pulumiDir.output
      container: {
        env: {
          AWS_ACCESS_KEY_ID: client.env.AWS_ACCESS_KEY_ID
          AWS_SECRET_ACCESS_KEY: client.env.AWS_SECRET_ACCESS_KEY
          AWS_REGION: "us-west-2"
          GOMODCACHE: _modCachePath
        }
        mounts: {
          "lambdazip": core.#Mount & {
            dest: "/lambda"
            contents: buildServer.output
            ro: true
          }
          "go mod cache": {
            contents: core.#CacheDir & {
              id: "pulumi_mod"
            }
            dest: _modCachePath
          }
          "go build cache": {
            contents: core.#CacheDir & {
              id: "pulumi_build"
            }
            dest: _buildCachePath
          }
          "pulumi plugin cache": {
            contents: core.#CacheDir & {
              id: "pulumi_plugin"
            }
            dest: _pulumiPluginCachePath
          }
        }
      }
    }
  }
}

#IntegTest: {
  clientBin: dagger.#FS
  serverUrl: string
  expectedOutput: string

  _image: alpine.#Build

  _run: core.#Exec & {
    input: _image.output.rootfs
    env: {
      "TEST_LAMBDA_URL": serverUrl
      "EXPECTED_OUTPUT": expectedOutput
    }
    args: ["sh", "-e", "-c", """
    OUTPUT="$(/clientBin/client $TEST_LAMBDA_URL)"
    if [ "$OUTPUT" != "$EXPECTED_OUTPUT" ]; then
      echo Expected \\"$EXPECTED_OUTPUT\\" but got \\"$OUTPUT\\"
      exit 1
    fi
    """]
    mounts: {
      "client": core.#Mount & {
        dest: "/clientBin"
        contents: clientBin
        ro: true
      }
    }
  }
  output: _run.output
}

#Zip: {
  zipInput: dagger.#FS
  subpath?: string
  zipName: string
  output: dagger.#FS

  _image: alpine.#Build & {
    packages: {
      "zip": _
    }
  }

  _run: core.#Exec & {
    input: _image.output.rootfs
    mounts: {
      "input": core.#Mount & {
        dest: "/mnt"
        contents: zipInput
        source: subpath
        ro: true
      }
    }
    workdir: "/mnt"
    args: ["zip", "-r", "/\(zipName)", "."]
  }
  _runDiff: core.#Diff & {
    lower: _image.output.rootfs
    upper: _run.output
  }
  output: _runDiff.output
}

