package core

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/Khan/genqlient/graphql"
	"github.com/containerd/containerd/platforms"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"go.dagger.io/dagger/engine"
	"go.dagger.io/dagger/sdk/go/dagger"
)

func TestMultiplatformImageExec(t *testing.T) {
	t.Parallel()

	err := engine.Start(context.Background(), nil, func(ctx engine.Context) error {
		res, err := multiplatformImages(ctx, "alpine:latest")
		require.NoError(t, err)

		require.Equal(t, "aarch64\n", res.Aarch64.Core.Image.Exec.Stdout)
		require.Equal(t, "x86_64\n", res.X86.Core.Image.Exec.Stdout)
		require.Equal(t, "s390x\n", res.S390x.Core.Image.Exec.Stdout)

		require.Equal(t,
			platforms.Normalize(ctx.HostPlatform).Architecture,
			platforms.Normalize(specs.Platform{
				Architecture: strings.TrimSpace(res.Host.Image.Exec.Stdout),
			}).Architecture,
		)

		arch := "arm64"
		root := res.Aarch64.Core.Image.Exec.Fs.ID
		if runtime.GOARCH == "arm64" {
			arch = "amd64"
			root = res.X86.Core.Image.Exec.Fs.ID
		}
		testMountRes := struct {
			Core struct {
				Filesystem struct {
					Exec struct {
						FS struct {
							Exec struct {
								Stdout string
							}
						}
					}
				}
			}
		}{}
		err = ctx.Client.MakeRequest(ctx,
			&graphql.Request{
				Query: `query TestMount($arch: String!, $root: FSID!) {
					withArchitecture(architecture: $arch) {
						core {
							filesystem(id: $root) {
								exec(input:{args: ["apk", "add", "file"]}) {
									fs {
										exec(input: {
											args: ["file", "/bin/busybox", "/mnt/bin/busybox"]
											mounts: [
												{fs: $root, path: "/mnt"},
											]
										}) {
											stdout
										}
									}
								}
							}
						}
					}
				}`,
				Variables: map[string]any{
					"arch": arch,
					"root": root,
				},
			},
			&graphql.Response{Data: &testMountRes},
		)
		require.NoError(t, err)
		return nil
	})
	require.NoError(t, err)
}

func TestMultiplatformExtension(t *testing.T) {
	startOpts := &engine.Config{
		Workdir:    "../../",
		ConfigPath: "core/integration/testdata/multiplatform/cloak.yaml",
	}

	err := engine.Start(context.Background(), startOpts, func(ctx engine.Context) error {
		testArchRes := struct {
			Multiplatform struct {
				Arch string
			}
		}{}
		err := ctx.Client.MakeRequest(ctx,
			&graphql.Request{
				Query: `{
					multiplatform {
						arch
					}
				}`,
			},
			&graphql.Response{Data: &testArchRes},
		)
		require.NoError(t, err)

		require.Equal(t,
			platforms.Normalize(ctx.HostPlatform).Architecture,
			platforms.Normalize(specs.Platform{
				Architecture: strings.TrimSpace(testArchRes.Multiplatform.Arch),
			}).Architecture,
		)

		gitRes := struct {
			Core struct {
				Git struct {
					ID dagger.FSID
				}
			}
		}{}
		err = ctx.Client.MakeRequest(ctx,
			&graphql.Request{
				Query: `{
					core {
						git(remote:"github.com/dagger/dagger", ref:"cloak") {
							id
						}
					}
				}`,
			},
			&graphql.Response{Data: &gitRes},
		)
		require.NoError(t, err)

		arch := "arm64"
		if runtime.GOARCH == "arm64" {
			arch = "amd64"
		}
		testEmulateCompileRes, err := call(ctx,
			`query TestEmulateCompile($src: FSID!, $arch: String!) {
				withArchitecture(architecture: $arch) {
					multiplatform {
						emulateCompile(src: $src, subpath: "core/shim/cmd") {
							fs {
								id
							}
							stdout
						}
					}
				}
			}`,
			map[string]any{
				"src":  gitRes.Core.Git.ID,
				"arch": arch,
			},
			struct {
				WithArchitecture struct {
					Multiplatform struct {
						EmulateCompile struct {
							Stdout string
							Fs     struct {
								ID dagger.FSID
							}
						}
					}
				}
			}{},
		)
		require.NoError(t, err)
		lines := strings.Split(testEmulateCompileRes.WithArchitecture.Multiplatform.EmulateCompile.Stdout, "\n")
		require.Equal(t,
			platforms.Normalize(specs.Platform{
				Architecture: arch,
			}).Architecture,
			platforms.Normalize(specs.Platform{
				Architecture: lines[0],
			}).Architecture,
		)
		require.Contains(t, lines, fmt.Sprintf("\tbuild\tGOARCH=%s",
			platforms.Normalize(specs.Platform{
				Architecture: arch,
			}).Architecture,
		))

		testCrossCompileRes, err := call(ctx,
			`query TestCrossCompile($src: FSID!, $arch: String!) {
				withArchitecture(architecture: $arch) {
					multiplatform {
						crossCompile(src: $src, subpath: "core/shim/cmd") {
							fs {
								id
							}
							stdout
						}
					}
				}
			}`,
			map[string]any{
				"src":  gitRes.Core.Git.ID,
				"arch": arch,
			},
			struct {
				WithArchitecture struct {
					Multiplatform struct {
						CrossCompile struct {
							Stdout string
							Fs     struct {
								ID dagger.FSID
							}
						}
					}
				}
			}{},
		)
		require.NoError(t, err)
		lines = strings.Split(testCrossCompileRes.WithArchitecture.Multiplatform.CrossCompile.Stdout, "\n")
		require.Equal(t,
			platforms.Normalize(specs.Platform{
				Architecture: runtime.GOARCH,
			}).Architecture,
			platforms.Normalize(specs.Platform{
				Architecture: lines[0],
			}).Architecture,
		)
		require.Contains(t, lines, fmt.Sprintf("\tbuild\tGOARCH=%s",
			platforms.Normalize(specs.Platform{
				Architecture: arch,
			}).Architecture,
		))
		return nil
	})
	require.NoError(t, err)
}

func TestMultiplatformImagePush(t *testing.T) {
	// FIXME:(sipsma) this test is a bit hacky+brittle, but unless we push to a real registry
	// or flesh out the idea of local services, it's probably the best we can do for now.

	err := engine.Start(context.Background(), nil, func(ctx engine.Context) error {
		go func() {
			err := runRegistry(ctx)
			if err != nil {
				t.Logf("error running registry: %v", err)
			}
		}()

		err := waitForRegistry(ctx)
		require.NoError(t, err)

		res, err := multiplatformImages(ctx, "alpine:latest")
		require.NoError(t, err)

		testRef := "127.0.0.1:5000/testmultiplatformimagepush:latest"
		err = ctx.Client.MakeRequest(ctx,
			&graphql.Request{
				Query: `query TestMultiplatformImagePush($ref: String!, $armFS: FSID!, $amdFS: FSID!, $s390xFS: FSID!) {
					core {
						pushMultiplatformImage(ref: $ref, filesystems: [$armFS, $amdFS, $s390xFS])
					}
				}`,
				Variables: map[string]any{
					"ref":     testRef,
					"armFS":   res.Aarch64.Core.Image.Exec.Fs.ID,
					"amdFS":   res.X86.Core.Image.Exec.Fs.ID,
					"s390xFS": res.S390x.Core.Image.Exec.Fs.ID,
				},
			},
			&graphql.Response{Data: new(map[string]any)},
		)
		require.NoError(t, err)

		/*
			FIXME:(sipsma) this triggers a nil pointer exception in buildkit: https://github.com/moby/buildkit/issues/3134
			res, err = multiplatformImages(ctx, testRef)
			require.NoError(t, err)

			require.Equal(t, "aarch64\n", res.Aarch64.Core.Image.Exec.Stdout)
			require.Equal(t, "x86_64\n", res.X86.Core.Image.Exec.Stdout)
			require.Equal(t, "s390x\n", res.S390x.Core.Image.Exec.Stdout)

			require.Equal(t,
				platforms.Normalize(ctx.HostPlatform).Architecture,
				platforms.Normalize(specs.Platform{
					Architecture: strings.TrimSpace(res.Host.Image.Exec.Stdout),
				}).Architecture,
			)
		*/

		return nil
	})
	require.NoError(t, err)
}

func call[R any](ctx engine.Context, query string, variables map[string]any, res R) (*R, error) {
	err := ctx.Client.MakeRequest(ctx,
		&graphql.Request{
			Query:     query,
			Variables: variables,
		},
		&graphql.Response{Data: &res},
	)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func multiplatformImages(ctx engine.Context, ref string) (res struct {
	Host struct {
		Image struct {
			Exec struct {
				Stdout string
				Fs     struct {
					ID string
				}
			}
		}
	}
	Aarch64 struct {
		Core struct {
			Image struct {
				Exec struct {
					Stdout string
					Fs     struct {
						ID string
					}
				}
			}
		}
	} `json:"aarch64"`
	X86 struct {
		Core struct {
			Image struct {
				Exec struct {
					Stdout string
					Fs     struct {
						ID string
					}
				}
			}
		}
	} `json:"x86"`
	S390x struct {
		Core struct {
			Image struct {
				Exec struct {
					Stdout string
					Fs     struct {
						ID string
					}
				}
			}
		}
	} `json:"s390x"`
}, rerr error) {
	err := ctx.Client.MakeRequest(ctx,
		&graphql.Request{
			Query: `query MultiplatformImages($ref: String!) {
				host: core {
					image(ref: $ref) {
						exec(input: {args: ["uname", "-m"]}) {
							stdout
							fs {
								id
							}
						}
					}
				}
				aarch64: withArchitecture(architecture: "arm64") {
					core {
						image(ref: $ref) {
							exec(input: {args: ["uname", "-m"]}) {
								stdout
								fs {
									id
								}
							}
						}
					}
				}
				x86: withArchitecture(architecture: "amd64") {
					core {
						image(ref: $ref) {
							exec(input: {args: ["uname", "-m"]}) {
								stdout
								fs {
									id
								}
							}
						}
					}
				}
				s390x: withArchitecture(architecture: "s390x") {
					core {
						image(ref: $ref) {
							exec(input: {args: ["uname", "-m"]}) {
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
				"ref": ref,
			},
		},
		&graphql.Response{Data: &res},
	)
	return res, err
}
