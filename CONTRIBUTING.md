# Contributing

## Scope and worktrees

Every change starts with a scoped Linear issue in the `delegatd` project. Work from a dedicated worktree based on the current `main`; keep the primary checkout unchanged. Keep changes vertical and end at an observable behavior or a safe rejection.

Use public-boundary TDD: start with the narrowest failing test, make it pass with the smallest correct change, and run the focused test before the relevant broader gates. Before publishing, run:

```sh
make verify
```

For the release path, also run:

```sh
make release-check VERSION=0.0.0
```

The pull-request template is part of the gate. It requires the matching Linear closing reference, observable behavior, focused red/green evidence, the final `make verify` result, provenance, identity, security effects, and rollback by revert.

## Identity and pull requests

The trusted coordinator is the only component allowed to mint or use the repository-scoped GitHub App token. It may push ordinary feature branches and create or update the App-authored pull request, using only the permissions required for that operation. Credentials, tokens, and private keys stay outside the repository and agent workspaces.

An agent never uses the owner credential to push, approve, or merge. The coordinator uses the dedicated `delegatd-agent-1352096391` App identity for ordinary branch publication and pull-request creation. Because that App deliberately lacks Workflows permission, the human owner pushes any branch containing `.github/workflows/**`; the owner then performs every approval and merge.

After the final push, the human owner reviews the complete pull request and approves it fresh. Before completing the squash, inspect GitHub’s generated squash commit message and remove any auto-generated `Co-authored-by:` trailers; co-author trails are not permitted. Merges are squash-only and performed by the owner after all required checks and CODEOWNER review pass. Do not enable automatic merging or bypass the required gate.

## Releases

Release setup publishes nothing. The human owner creates and pushes an annotated SSH-signed `vMAJOR.MINOR.PATCH` tag from a commit on `main`. The release workflow verifies the owner signature, builds the six target archives and SPDX documents deterministically, produces checksums, and publishes only after GitHub provenance attestation succeeds.

The agent and coordinator do not read or export the owner’s signing key. A failed release leaves its draft for inspection; recover by correcting the cause and rerunning, or roll back repository changes by revert.
