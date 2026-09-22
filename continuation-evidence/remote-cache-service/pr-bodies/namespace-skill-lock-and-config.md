## Summary

Two fixes to the Namespace access skill's setup script for hosts where several agents run the skill at once. Both came from failures seen on a shared host: a started `tailscaled` kept the agents' shared lock and blocked every later call, and concurrent setup runs corrupted the AWS config.

## Start tailscaled without the caller's descriptors

Setup starts a long-lived userspace `tailscaled` when none is running, and the daemon inherited every descriptor open in the caller. With calls serialized by `flock <lock> <script>`, the daemon kept the lock after the script exited, so every later skill call on the host blocked until the daemon was killed. Setup now closes the descriptors above stderr in a background subshell and execs the daemon from it, so the pid file still records the daemon itself. SKILL.md asks concurrent callers to use `flock -o`, which keeps the lock out of the script entirely; `flock` is not installed on macOS by default.

## Write the AWS config atomically

Setup rewrote the AWS config in place on every run, and concurrent runs could read a partly written file and write it back. The config is now written to a temporary file in the same directory and replaces the original in one step, keeping existing permission bits and replacing a symlink's target rather than the link. This prevents partial and interleaved files; it does not merge independent concurrent edits, so concurrent callers should still share one lock.

## Validation

On Linux, with a temporary lock and a stand-in daemon started under plain `flock`, the old start left the lock held by the daemon and the new start left the daemon with descriptors 0-2 only and the lock free. Forty concurrent writers over five rounds against a config that also holds an unrelated profile: the in-place write lost that profile in every round; the atomic write kept every section in all five. ShellCheck 0.9.0 reports no diagnostics. macOS compatibility (`/dev/fd` enumeration, Bash 3.2 descriptor close, `nohup` exec) was reviewed against Apple's sources, not run.
