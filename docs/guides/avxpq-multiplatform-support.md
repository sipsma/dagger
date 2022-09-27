---
slug: /avxpq/multiplatform-support
displayed_sidebar: "0.3"
---

# Multiplatform Support

## Concepts

### Platform

- A combination of OS and CPU architecture that executable code may target.
- OCI Images support pulling+pushing images with layers for different platforms all bundled together ([see spec](https://github.com/opencontainers/image-spec/blob/main/image-index.md#image-index-property-descriptions))

### Emulation

- A technique by which a binary built to target one CPU architecture can be executed on a different CPU architecture via automatic conversion of machine code.
- Typically slow and only used when cross-compilation is not an option.

### Cross-Compilation

- A technique by which you can build a binary that targets Platform A on a machine of Platform B. I.e. cross-compilation enables you to build a Windows x86_64 binary from a Linux aarch64 host.
- Results in binaries that don't need to take the performance hit of emulation at runtime.

### Host Architecture

- The architecture of the CPU on which Dagger executes containers.

### Target Architecture

- The architecture of filesystems that Dagger expects as inputs and outputs during a given query. May or may not match the host architecture.

## Usage

### Setup

To exec binaries on filesystems that target a different platform than that of the host, `binfmt_misc` needs to be configured on the host kernel (on the machine where Dagger will run containers).

If the host is running docker, this one-liner will setup everything (only needed one time):

```sh
docker run --privileged --rm tonistiigi/binfmt --install all
```

[See the docs here for more info](https://github.com/tonistiigi/binfmt/).

### Pulling Platform-specific Images

Example of pulling the amd64 (a.k.a x86_64) alpine image (works even on hosts of different architectures):

```graphql
{
  withArchitecture(architecture: "amd64") {
    core {
      image(ref: "alpine:latest") {
        exec(input: { args: ["uname", "-m"] }) {
          stdout
        }
      }
    }
  }
}
```

### Pushing Multiplatform Images

NOTE: this is a _very_ temporary solution. The better solution will involve an API type that can bundle multiple platform-specific versions of a filesystem together (see Future Changes section).

```graphql
query MultiplatformImagePush(
  $ref: String!
  $armFS: FSID!
  $amdFS: FSID!
  $s390xFS: FSID!
) {
  core {
    pushMultiplatformImage(
      ref: "localhost:5000/mymultiplatimage:latest"
      filesystems: [$armFS, $amdFS, $s390xFS]
    )
  }
}
```

### Emulated Compilation Example

If `withArchitecture` is used then all core exec calls will be executed with the provided architecture, even if it doesn't match the host. E.g.

```graphql
{
  withArchitecture(architecture: "amd64") {
    core {
      image(ref: "myimage:latest") {
        exec(input: { args: ["build", "something"] }) {
          fs {
            id
          }
        }
      }
    }
  }
}
```

Will result in `build something` being called on the amd64 `myimage` even if the Dagger host is of a different architecture.

[See also our test case for slightly more involved examples (the `emulateCompile` method specifically)](https://github.com/dagger/dagger/tree/cloak/core/integration/testdata/multiplatform/ext/main.go)

### Cross-Compilation Example

Two parts to this:

1. You can obtain the current target platform via the `architecture` API
1. You can force a query to run as the host platform by wrapping it in `withArchitecture(architecture: "host")`

With those two features, you can create an extension that can force efficient host-based cross-compilation even when the target platform is not the same as the host. E.g. you can run this from an extension:

```graphql
{
  architecture
}
```

to get `$targetArch` and then provide that var to:

```graphql
query CrossCompile($targetArch: String!) {
  withArchitecture(architecture: "host") {
    core {
      image(ref: "myimage:latest") {
        exec(
          input: {
            args: ["build", "something"]
            env: [{ name: "TARGET_ARCH", value: $targetArch }]
          }
        ) {
          fs {
            id
          }
        }
      }
    }
  }
}
```

That will result in `build something` being called on the host-archiecture `myimage` but with the compilation setup to target `$targetArch`.

This approach enables callers to specify a target architecture but still allow extensions to selectively use efficient cross-compilation as possible.

[See also our test case for slightly more involved examples (the `crossCompile` method specifically)](https://github.com/dagger/dagger/tree/cloak/core/integration/testdata/multiplatform/ext/main.go)

## Rules of Multiplatform on Dagger

1. A `Filesystem` may be associated with a given platform or it may be platform-agnostic.
   - The "alpine" container image is an example of a filesystem associated with a given platform (the platform the binaries within it have targeted)
   - A filesystem containing only plain text files is an example of a filesystem that will typically be platform-agnostic
   - **_Caveat_** there's not yet a way to force a filesystem to be marked as associated with a given platform or as platform agnostic; we currently just inherit all the implicit rules from LLB (i.e. layers inherit from their parents, FileOps are platform-agnostic, images are platform-specific, etc.).
1. Each query submitted to Dagger is associated with a Target Architecture
   - This value can be explicitly set in the query via the `withArchitecture` API
   - Otherwise, the value is inherited from the environment (specifically, the `DAGGER_SESSION_CONTEXT` set by the Dagger engine during extension execution)
   - Finally, the value will default to that of the Host Architecture
1. All of the input and output Filesystem objects in a given query must be either the Target Architecture or be platform agnostic.
   - If an extension returns a Filesystem that isn't the target architecture or agnostic, an error will result.
   - **_Caveat_** this is not yet enforced in the current implementation; it's just aspirational. For example, the current `pushMultiplatformImage` API is incompatible with this rule (see Future Changes section for details on longer-term plan).
1. Non-core Extensions always execute as the Host Architecture. Only `core` APIs can run as non-host architectures.
   - However, when an extension executes, it inherits the target architecture for any queries _it_ submits (via the `DAGGER_SESSION_CONTEXT` env var).
   - This is chosen as the default as it's what's wanted in the _vast_ majority of cases.
     - The only possible case where you may want an extension to execute as a non-host architecture would be if it's directly executing code in the extension container and outputting new filesystems created as a result. For now, these cases will just need to call out to core APIs instead (though it's possible to lift this restriction in the future if needed)

## Future Changes

1. Support for specifying multiple target architectures in a single query (highly related to below point)
1. Support for bundling multiple platform versions of a single filesystem into one API object.
   - This will fit nicely with the rest of the API changes that migrate from Filesystem to Container/Directory/etc.
   - This will also enable us to get rid of the temporary `pushMultiplatformImage` API
1. (Someday) Support for executing with different OSes (in addition to current support for different architectures)
