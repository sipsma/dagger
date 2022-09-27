package main

import (
	"context"
	"fmt"
	"path"
	"runtime"

	"github.com/Khan/genqlient/graphql"
	"go.dagger.io/dagger/sdk/go/dagger"
)

func (r *multiplatform) arch(ctx context.Context, in *string) (string, error) {
	return runtime.GOARCH, nil
}

func (r *multiplatform) emulateCompile(ctx context.Context, src dagger.FSID, subpath *string) (*CompileOutput, error) {
	client, err := dagger.Client(ctx)
	if err != nil {
		return nil, err
	}

	if subpath == nil {
		empty := ""
		subpath = &empty
	}

	res := struct {
		Core struct {
			Image struct {
				Exec CompileOutput
			}
		}
	}{}
	err = client.MakeRequest(ctx,
		&graphql.Request{
			Query: `query Build($src: FSID!, $cmd: String!) {
				core {
					image(ref: "golang:1.19.1-alpine") {
						exec(input:{
							args: ["sh", "-c", $cmd],
							workdir: "/src"
							mounts: [
								{fs: $src, path: "/src"},
							]
						}) {
							stdout
							fs {
								id
							}
						}
					}
				}
			}`,
			Variables: map[string]any{
				"src": src,
				"cmd": fmt.Sprintf("uname -m && /usr/local/go/bin/go build -o /out/main %s && /usr/local/go/bin/go version -m /out/main", path.Join("/src", *subpath)),
			},
		},
		&graphql.Response{Data: &res},
	)
	if err != nil {
		return nil, err
	}
	return &res.Core.Image.Exec, nil
}

func (r *multiplatform) crossCompile(ctx context.Context, src dagger.FSID, subpath *string) (*CompileOutput, error) {
	client, err := dagger.Client(ctx)
	if err != nil {
		return nil, err
	}

	if subpath == nil {
		empty := ""
		subpath = &empty
	}

	architectureRes := struct {
		Architecture string
	}{}
	err = client.MakeRequest(ctx,
		&graphql.Request{
			Query: `{
				architecture
			}`,
		},
		&graphql.Response{Data: &architectureRes},
	)
	if err != nil {
		return nil, err
	}
	arch := architectureRes.Architecture

	buildRes := struct {
		WithArchitecture struct {
			Core struct {
				Image struct {
					Exec CompileOutput
				}
			}
		}
	}{}
	err = client.MakeRequest(ctx,
		&graphql.Request{
			Query: `query Build($src: FSID!, $cmd: String!, $plt: String!) {
				withArchitecture(architecture: "host") {
					core {
						image(ref: "crazymax/goxx:latest") {
							exec(input:{
								args: ["sh", "-c", $cmd],
								workdir: "/src"
								env: [
									{name: "PATH", value: "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
									{name: "TARGETPLATFORM", value: $plt},
								]
								mounts: [
									{fs: $src, path: "/src"},
								]
							}) {
								stdout
								fs {
									id
								}
							}
						}
					}
				}
			}`,
			Variables: map[string]any{
				"src": src,
				"cmd": fmt.Sprintf("uname -m && goxx-go build -o /out/main %s && go version -m /out/main", path.Join("/src", *subpath)),
				"plt": "darwin/" + arch,
			},
		},
		&graphql.Response{Data: &buildRes},
	)
	if err != nil {
		return nil, err
	}
	return &buildRes.WithArchitecture.Core.Image.Exec, nil
}
