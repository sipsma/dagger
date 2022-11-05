import hashlib
import logging
import os
from pathlib import Path
import sys
import tempfile
from asyncio.subprocess import Process
from subprocess import DEVNULL, CalledProcessError, PIPE, STDOUT
from urllib.parse import urlparse

import anyio
from aiohttp import ClientTimeout
from attrs import Factory, define, field
from gql.transport import AsyncTransport, Transport
from gql.transport.aiohttp import AIOHTTPTransport
from gql.transport.requests import RequestsHTTPTransport

from dagger import Client

from .base import Config, Connector, register_connector

logger = logging.getLogger(__name__)

normalized_arch = {
  "x86_64": "amd64",
  "amd64": "amd64",
  "aarch64": "arm64",
  "arm64": "arm64",
}

@define
class Engine:
    cfg: Config

    _proc: Process | None = field(default=None, init=False)

    async def start(self) -> None:
        cache_dir = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "dagger"
        cache_dir.mkdir(parents=True, exist_ok=True, mode=0o700)

        # TODO: this should be a config option obviously
        image_ref = "docker.io/eriksipsma/test-dagger:rebase@sha256:b6bcf40346a70a834a0c1461046551993b17c92288c32121b171a825958e7901"

        # TODO: not sure where consts usually go
        digest_len = 16

        # Check to see if image_ref contains @sha256:, if so use the digest as the id.
        is_pinned = False
        if "@sha256:" in image_ref:
          id = image_ref.split("@sha256:", maxsplit=1)[1]
          # TODO: add verification that the digest is valid (not something malicious with / or ..)
          is_pinned = True
        else:
          # set id to the sha256 hash of the image_ref
          # TODO: ensure that this is consistent w/ Go's sha256 hash (encoding is only likely source of difference)
          id = hashlib.sha256(image_ref.encode()).hexdigest()
        id = id[:digest_len]

        helper_bin_path = cache_dir / ("dagger-sdk-helper-" + id)
        container_name = "dagger-engine-" + id

        docker_run_args = [
            "docker", "run",
            "--name", container_name,
            "-d",
            "--restart", "always",
            "--privileged",
            image_ref,
            "--debug",
        ]
        docker_run_result = await anyio.run_process(
            docker_run_args, 
            stdout=PIPE, 
            stderr=STDOUT,
            check=False,
        )
        if docker_run_result.returncode != 0:
          if f"Conflict. The container name \"/{container_name}\" is already in use by container" not in docker_run_result.stdout.decode():
            raise Exception("Failed to start engine container: " + docker_run_result.stdout.decode())

        # TODO: garbage collection of old containers

        uname = os.uname()
        self_arch = normalized_arch.get(uname.machine.lower())
        self_os = uname.sysname.lower()

        if not helper_bin_path.exists():
          tmp_bin = tempfile.NamedTemporaryFile(prefix='dagger-sdk-helper-', dir=cache_dir, delete=True)
          docker_cp_args = [
              "docker", "cp",
              container_name + f":/usr/bin/dagger-sdk-helper-{self_os}-{self_arch}",
              tmp_bin.name,
          ]
          docker_cp_result = await anyio.run_process(
              docker_cp_args, 
              stdout=PIPE, 
              stderr=STDOUT,
              check=False,
          )
          if docker_cp_result.returncode != 0:
            raise Exception("Failed to copy helper binary: " + docker_cp_result.stdout.decode())
          os.chmod(tmp_bin.name, 0o700)
          if is_pinned:
            os.rename(tmp_bin.name, helper_bin_path)
          else:
            helper_bin_path = Path(tmp_bin.name)

        # TODO: garbage collection of old helper binaries

        buildkit_host = "docker-container://" + container_name

        helper_args = [helper_bin_path, "--remote", buildkit_host]
        if self.cfg.workdir:
            helper_args.extend(["--workdir", str(self.cfg.workdir.absolute())])
        if self.cfg.config_path:
            helper_args.extend(["--project", str(self.cfg.config_path.absolute())])

        # TODO: log output should be configurable, not hardcoded to our stderr
        stdoutr, stdoutw = os.pipe()
        stdoutr = os.fdopen(stdoutr)
        self._proc = await anyio.open_process(helper_args, stdin=PIPE, stdout=stdoutw, stderr=sys.stderr)

        # read port number from first line of stdout
        port = int(stdoutr.readline().strip())
        stdoutr.close()
        # TODO: verify port number is valid

        self.cfg.host = f"http://localhost:{port}"

    async def stop(self) -> None:
        assert self._proc is not None
        # FIXME: make sure signals from OS are being handled
        self._proc.terminate()
        # Gives 5 seconds for the process to terminate properly
        # FIXME: timeout
        await self._proc.wait()
        # FIXME: kill?


@register_connector("http")
@define
class HTTPConnector(Connector):
    """Connect to dagger engine via HTTP"""

    engine: Engine = Factory(lambda self: Engine(self.cfg), takes_self=True)

    @property
    def query_url(self) -> str:
        return f"{self.cfg.host.geturl()}/query"

    async def connect(self) -> Client:
        if self.cfg.host.hostname == "localhost":
            await self.provision()
        return await super().connect()

    async def provision(self) -> None:
        # FIXME: handle cancellation, retries and timeout
        # FIXME: handle errors during provisioning
        await self.engine.start()

    async def close(self) -> None:
        # FIXME: need exit stack?
        await super().close()
        # await self.engine.stop()

    def connect_sync(self) -> Client:
        # FIXME: provision engine in sync
        return super().connect_sync()

    def make_transport(self) -> AsyncTransport:
        session_timeout = self.cfg.execute_timeout
        if isinstance(session_timeout, int):
            session_timeout = float(session_timeout)
        return AIOHTTPTransport(
            self.query_url,
            timeout=self.cfg.timeout,
            client_session_args={"timeout": ClientTimeout(total=session_timeout)},
        )

    def make_sync_transport(self) -> Transport:
        return RequestsHTTPTransport(self.query_url, timeout=self.cfg.timeout, retries=10)
