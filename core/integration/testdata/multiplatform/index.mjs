import { gql, Engine } from "@dagger.io/dagger";

new Engine({
  Workdir: "/home/sipsma/repo/github.com/sipsma/dagger/",
  ConfigPath: "core/integration/testdata/multiplatform/cloak.yaml",
}).run(async (client) => {
  const daggerRepo = await client
    .request(
      gql`
        query {
          core {
            git(remote: "github.com/dagger/dagger", ref: "cloak") {
              id
            }
          }
        }
      `
    )
    .then((result) => result.core.git.id);

  const buildResult = await client
    .request(
      gql`
        query ($src: FSID!) {
          withArchitecture(architecture: "amd64") {
            multiplatform {
              crossCompile(src: $src, subpath: "cmd/cloak/") {
                fs {
                  id
                }
              }
            }
          }
        }
      `,
      {
        src: daggerRepo,
      }
    )
    .then((result) => result.withArchitecture.multiplatform.crossCompile.fs.id);

  await client
    .request(
      gql`
        query ($content: FSID!) {
          host {
            workdir {
              write(
                path: "core/integration/testdata/multiplatform"
                contents: $content
              )
            }
          }
        }
      `,
      {
        content: buildResult,
      }
    )
    .then((result) => result.host.workdir.write);
});
