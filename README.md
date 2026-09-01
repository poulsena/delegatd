# delegatd

`delegatd` is a self-hosted Go control plane for governed agent work. The first
tracer is a read-only dependency diagnosis.

## Prerequisites

- Go 1.27.0 for development (the release image uses the pinned toolchain).
- A Docker client connected to a Linux Docker server.
- The `omp` executable with RPC mode support.
- A CGO-free SQLite-compatible state database file.
- A GitHub App ID and RSA private key. The key file must be a regular file with
  owner-only permissions (`0600` or stricter on Unix).

For the doctor-only GitHub capability, the App needs no repository or
organization permissions and no write permissions. The probe authenticates the
App with a JWT and performs only `GET /app`; installation access and repository
publication permissions are intentionally outside this tracer.

Copy `examples/delegatd.yaml`, replace the placeholder App ID and key, and keep
relative paths relative to the configuration file's directory. The configuration
loader rejects unknown fields, future schema fields, inline private keys, and
environment interpolation.

Create an empty SQLite file before diagnosis; `doctor` never creates or migrates
one. For example, with the SQLite CLI:

```sh
sqlite3 /absolute/path/state.db 'PRAGMA user_version;'
```

## Diagnosis

```text
delegatd doctor --config FILE [--timeout DURATION]
```

`--config` is required. `--timeout` defaults to `10s` and must be positive.
`doctor --help` prints usage. Exit status `0` means every check passed, `1`
means configuration or a dependency diagnosis failed, and `2` means invalid
CLI usage. Results are deterministic, line-oriented, and redact failure causes:

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

It performs no external write. In particular, this tracer does not verify
repository installation or publication permissions, create or migrate the store,
onboard resources, persist task state, or run agent work.
