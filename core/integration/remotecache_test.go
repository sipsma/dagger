package core

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"dagger.io/dagger"
	"github.com/moby/buildkit/identity"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestRemoteCacheRegistry(t *testing.T) {
	// TODO: until this setting is configurable at runtime, just spawning separate engines w/ the config set
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registryContainerName := runRegistryInDocker(ctx, t)
	getClient := func() *dagger.Client {
		return runSeparateEngine(ctx, t, map[string]string{
			"_EXPERIMENTAL_DAGGER_CACHE_CONFIG": "type=registry,ref=127.0.0.1:5000/test-cache,mode=max",
		}, "container:"+registryContainerName)
	}

	pipelineOutput := func(c *dagger.Client) string {
		output, err := c.Container().From("alpine:3.17").WithExec([]string{
			"sh", "-c", "head -c 128 /dev/random | sha256sum",
		}).Stdout(ctx)
		require.NoError(t, err)
		return output
	}

	/*
		1. Start a registry for storing the cache
		2. Start two independent engines from empty cache that are configured to use the registry as remote cache backend
		3. Run an exec w/ output from /dev/random in the first engine
		4. Close the first engine's client, flushing the remote cache for the session
		5. Run the same exec in the second engine, verify it imports the cache and output the same value as the first engine
	*/
	clientA := getClient()
	clientB := getClient()
	outputA := pipelineOutput(clientA)
	require.NoError(t, clientA.Close())
	outputB := pipelineOutput(clientB)
	require.Equal(t, outputA, outputB)
}

func TestRemoteCacheS3(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("buildkit s3 caching", func(t *testing.T) {
		bucket := "dagger-test-remote-cache-s3-" + identity.NewID()
		s3ContainerName := runS3InDocker(ctx, t, bucket)
		getClient := func() *dagger.Client {
			return runSeparateEngine(ctx, t, map[string]string{
				"_EXPERIMENTAL_DAGGER_CACHE_CONFIG": "type=s3,mode=max,endpoint_url=http://localhost:9000,access_key_id=minioadmin,secret_access_key=minioadmin,region=mars,use_path_style=true,bucket=" + bucket,
			}, "container:"+s3ContainerName)
		}

		pipelineOutput := func(c *dagger.Client) string {
			output, err := c.Container().From("alpine:3.17").WithExec([]string{
				"sh", "-c", "head -c 128 /dev/random | sha256sum",
			}).Stdout(ctx)
			require.NoError(t, err)
			return output
		}

		/*
			1. Start an s3 compatible server (minio) locally for storing the cache
			2. Start two independent engines from empty cache that are configured to use s3 as remote cache backend
			3. Run an exec w/ output from /dev/random in the first engine
			4. Close the first engine's client, flushing the remote cache for the session
			5. Run the same exec in the second engine, verify it imports the cache and output the same value as the first engine
		*/
		clientA := getClient()
		clientB := getClient()
		outputA := pipelineOutput(clientA)
		require.NoError(t, clientA.Close())
		outputB := pipelineOutput(clientB)
		require.Equal(t, outputA, outputB)
	})

	t.Run("dagger s3 caching (with pooling)", func(t *testing.T) {
		bucket := "dagger-test-remote-cache-s3-" + identity.NewID()
		s3ContainerName := runS3InDocker(ctx, t, bucket)
		getClient := func() *dagger.Client {
			return runSeparateEngine(ctx, t, map[string]string{
				"_EXPERIMENTAL_DAGGER_CACHE_CONFIG": "type=experimental_dagger_s3,mode=max,endpoint_url=http://localhost:9000,access_key_id=minioadmin,secret_access_key=minioadmin,region=mars,use_path_style=true,bucket=" + bucket + ",prefix=test-cache-pool/",
			}, "container:"+s3ContainerName)
		}

		pipelineOutput := func(c *dagger.Client, id string) string {
			output, err := c.Container().
				From("alpine:3.17").
				WithEnvVariable("ID", id).
				WithExec([]string{
					"sh", "-c", "head -c 128 /dev/random | sha256sum",
				}).Stdout(ctx)
			require.NoError(t, err)
			return output
		}

		generatedOutputs := map[string]string{} // map of unique id set in exec -> output
		var mu sync.Mutex
		var eg errgroup.Group
		for i := 0; i < 5; i++ {
			eg.Go(func() error {
				id := identity.NewID()
				client := getClient()
				mu.Lock()
				defer mu.Unlock()
				generatedOutputs[id] = pipelineOutput(client, id)
				return client.Close()
			})
		}
		require.NoError(t, eg.Wait())
		eg = errgroup.Group{}
		client := getClient()
		for id, cachedOutput := range generatedOutputs {
			id, cachedOutput := id, cachedOutput
			eg.Go(func() error {
				require.Equal(t, cachedOutput, pipelineOutput(client, id))
				return nil
			})
		}
		require.NoError(t, eg.Wait())
		require.NoError(t, client.Close())
	})
}

func runS3InDocker(ctx context.Context, t *testing.T, bucket string) string {
	t.Helper()
	name := "dagger-test-remote-cache-s3-" + identity.NewID()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", name, "minio/minio", "server", "/data")
	t.Cleanup(func() {
		cmd.Process.Kill()
	})
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	t.Cleanup(func() {
		cmd.Process.Kill()
	})
	require.NoError(t, err)
	// wait for the s3 to be ready
	for i := 0; i < 100; i++ {
		cmd := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", "curl -s -o /dev/null -w '%{http_code}' http://localhost:9000/minio/health/live")
		out, err := cmd.CombinedOutput()
		if string(out) == "200" && err == nil {
			break
		}
		if i == 99 {
			t.Fatalf("minio s3 not ready: %v: %s", err, out)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// create the bucket
	cmd = exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "container:"+name, "--entrypoint", "sh", "minio/mc", "-c", "mc config host add minio http://localhost:9000 minioadmin minioadmin && mc mb minio/"+bucket)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	require.NoError(t, err)

	return name
}

// TODO: dedupe this w/ the services PR
func runRegistryInDocker(ctx context.Context, t *testing.T) string {
	t.Helper()
	name := "dagger-test-remote-cache-registry-" + identity.NewID()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", name, "registry:2")
	t.Cleanup(func() {
		cmd.Process.Kill()
	})
	err := cmd.Start()
	require.NoError(t, err)
	// wait for the registry to be ready
	for i := 0; i < 100; i++ {
		cmd := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", "wget -q -O - http://localhost:5000/v2/")
		out, err := cmd.CombinedOutput()
		if string(out) == "{}" && err == nil {
			break
		}
		if i == 99 {
			t.Fatalf("registry not ready: %v: %s", err, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return name
}

func runSeparateEngine(ctx context.Context, t *testing.T, env map[string]string, network string) *dagger.Client {
	t.Helper()
	name := "dagger-test-remote-cache-" + identity.NewID()

	allArgs := []string{"run"}
	dockerRunArgs := []string{
		"--rm",
		"-v", "/var/lib/dagger", // path is set in util/mage/engine.go
		"--privileged",
		"--name", name,
	}
	for k, v := range env {
		dockerRunArgs = append(dockerRunArgs, "-e", k+"="+v)
	}
	if network != "" {
		dockerRunArgs = append(dockerRunArgs, "--network", network)
	}
	allArgs = append(allArgs, dockerRunArgs...)
	allArgs = append(allArgs,
		"localhost/dagger-engine.dev:latest", // set in util/mage/engine.go
		"--debug",
	)

	cmd := exec.CommandContext(ctx, "docker", allArgs...)
	t.Cleanup(func() {
		cmd.Process.Kill()
	})
	err := cmd.Start()
	require.NoError(t, err)

	// NOTE: this isn't thread safe, don't run in parallel w/ other tests
	currentVal := os.Getenv("_EXPERIMENTAL_DAGGER_RUNNER_HOST")
	os.Setenv("_EXPERIMENTAL_DAGGER_RUNNER_HOST", "docker-container://"+name)
	defer os.Setenv("_EXPERIMENTAL_DAGGER_RUNNER_HOST", currentVal)

	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stdout))
	require.NoError(t, err)
	return c
}
