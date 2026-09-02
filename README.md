# delegatd

`delegatd` is a self-hosted Go control plane for governed agent work. The first
usable workflow accepts one manually submitted task for an explicitly
allowlisted repository, persists its normalized input and immutable snapshots,
and exposes the pending task for inspection. It does not execute the task.

## Prerequisites

- Go 1.27.0 for development and release builds.
- A GitHub App with Metadata: read and Contents: read permissions on the
  allowlisted repository. No write permission is needed by this workflow.
- A GitHub App ID and RSA private key. The key file must be a regular file with
  owner-only permissions (`0600` or stricter on Unix).
- A CGO-free SQLite-compatible state database file is not required in advance:
  `task submit` creates and migrates it. The parent directory must already
  exist and the state file is created with owner-only permissions.
- Docker and `omp` remain required by the deployment schema and by `doctor`,
  but `task submit` and `task show` do not start either one.

Copy `examples/delegatd.yaml`, replace the placeholder App ID, key, repository,
and state path, and keep relative paths relative to the configuration file's
directory. The deployment loader rejects unknown fields, future schema fields,
inline private keys, environment interpolation, and YAML aliases. Repository
`.delegatd.yaml` content is read at the pinned default-branch revision; a
missing file selects the empty version-one repository profile.

## Diagnosis

```text
delegatd doctor --config FILE [--timeout DURATION]
```

`--config` is required. `--timeout` defaults to `10s` and must be positive.
`doctor --help` prints usage. Exit status `0` means every check passed, `1`
means configuration or dependency diagnosis failed, and `2` means invalid CLI
usage. Results are deterministic, line-oriented, and redact failure causes:

```text
PASS config: schema version 1
PASS connector.github-main: GitHub App authenticated
PASS workspace_provider.docker-local: Docker server 29.7.2 (linux)
PASS agent_runtime.omp-primary: OMP RPC protocol 1
PASS store.sqlite: SQLite 3.51.0
PASS doctor: 4 checks passed
```

A failed dependency replaces only its own line and independent checks still run.
Configuration failures print a safe configuration reason followed by
`FAIL doctor: configuration invalid`.

The command performs only these external operations:

- GitHub `GET /app` using a short-lived signed App JWT;
- Docker `version` inspection;
- OMP no-session RPC startup and clean shutdown;
- read-only SQLite `PingContext` and metadata/version queries.

`doctor` never creates, migrates, or writes the state database.

## Manual tasks

Submit and inspect a task with:

```text
delegatd task submit --config FILE --resource NAME (--input TEXT | --input-file FILE)
delegatd task show --config FILE TASK_ID
```

`NAME` is a logical resource alias from the deployment `resources` allowlist;
the connector's external repository identity is not selected implicitly. Use
exactly one input source:

- `--input TEXT` for short instructions;
- `--input-file FILE` for a regular file;
- `--input-file -` to read standard input explicitly.

Input is capped at 1 MiB, must be valid UTF-8 without NUL bytes, converts CRLF
and bare CR to LF, removes only blank lines at the outer boundary, and preserves
all bytes on remaining lines. Empty input and invalid input are rejected. The
stored input envelope is versioned and does not retain the source file path.

On success, submit prints one compact JSON object:

```json
{"task_id":"task_<RFC4648-base32>"}
```

Show prints one compact JSON object containing the pending status, logical
resource projection, normalized input, repository configuration snapshot, and
effective policy snapshot. The snapshots are captured during submission;
show reads the task row and never re-fetches the repository or re-evaluates
current configuration. The effective policy starts deny-by-default, and the
initial audit history entry is `manual_submission`.

`task show` is an offline store-only operation. Its configuration may contain
only `version: 1` and the SQLite `store` section, and unrelated current
connector/resource/policy/workspace/runtime configuration is ignored. It works
after the submitting process exits and continues to return the original
snapshots after later repository or policy changes.

Task usage errors print the relevant usage to stderr and exit `2`. Operational
failures print `FAIL task: <safe reason>` to stdout and exit `1`; paths, input,
SQL, response bodies, credentials, and authorization headers are never shown.
The state store uses an application schema at `PRAGMA user_version = 1`, creates
the repository/task/history records atomically, and refuses unsupported or
unrelated existing schemas rather than adopting them.

This tracer persists and inspects pending tasks for allowlisted repositories.
It does not execute tasks, create runs, schedule, retry, resume, follow up,
start Docker or OMP, run validation commands, mutate repository content, or
publish external changes. Repository onboarding in this slice is the
connector-backed read-only snapshot of an explicitly configured allowlist; it
does not grant access beyond the GitHub App and `delegatd` resource policy.

## Verification and releases

Run the local gates before opening a pull request:

```sh
make verify
make release-check VERSION=0.0.0
```

The deterministic release check produces these six archives:

```text
delegatd_0.0.0_darwin_amd64.tar.gz
delegatd_0.0.0_darwin_arm64.tar.gz
delegatd_0.0.0_linux_amd64.tar.gz
delegatd_0.0.0_linux_arm64.tar.gz
delegatd_0.0.0_windows_amd64.zip
delegatd_0.0.0_windows_arm64.zip
```

Each archive is accompanied by a matching `.spdx.json` document and the release includes `SHA256SUMS`. Verify downloaded files with the platform-native checksum command:

```sh
# Linux
sha256sum -c SHA256SUMS

# macOS
shasum -a 256 -c SHA256SUMS
```

On Windows PowerShell, compare every line in `SHA256SUMS` with `Get-FileHash`:

```powershell
Get-Content .\SHA256SUMS | ForEach-Object {
  $expected, $file = $_ -split '  ', 2
  $actual = (Get-FileHash -Algorithm SHA256 $file).Hash.ToLowerInvariant()
  if ($actual -ne $expected) { throw "checksum mismatch: $file" }
}
```

Inspect every downloaded SPDX JSON document as a human, then verify GitHub provenance for every downloaded asset:

```sh
for asset in delegatd_0.0.0_*.tar.gz delegatd_0.0.0_*.zip \
  delegatd_0.0.0_*.spdx.json SHA256SUMS; do
  gh attestation verify "$asset" --repo poulsena/delegatd
done
```

No license is granted for this repository; all rights are reserved.
