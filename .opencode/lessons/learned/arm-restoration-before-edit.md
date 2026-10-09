# Arm restoration before requesting a mutation

- **Type:** pattern
- **Change:** reproducible production benchmark matrix
- **Failure:** a worker can replace a file successfully but lose its JSON reply
  during cancellation. Arming cleanup only after that reply loses the restore.
- **Rule:** retain original SHA, exact suffix and private backup path before
  launching the edit. Restore verifies backup SHA and original+suffix bytes;
  already-original bytes are an idempotent success. Wait for worker close before
  restoring and retain the backup on cleanup failure.
- **Limit:** no-follow reads/private atomic writes prevent symlink redirection,
  not every concurrent-write race. Require exclusive disposable roots; SIGKILL
  or machine loss still requires manual recovery. OS self-RSS and sampled child
  RSS have different availability/scopes and must not be added as one peak.
