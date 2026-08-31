# delegatd

`delegatd` is a self-hosted Go control plane for defining, running, supervising, and governing configurable agent work.

## Development loop

- Use TDD. Start each implementation change with a failing test at the narrowest public boundary that expresses the observable behavior. Make it pass with the smallest correct change, then run the focused test and the relevant broader checks.
- Work in vertical slices. Start at a real system boundary and finish at an observable result or safe rejection. Include the schema, policy check, audit record, failure handling, test, and documentation required by that slice.
- Prefer surgical edits over large refactors. Change only what the current slice requires; reserve a broad refactor for cases where the slice cannot be correct without it, and make that necessity explicit.
- Keep tracer bullets small, reviewable, and independently demonstrable. Split by scenario or guarantee, not by horizontal component phase.

## Architecture guardrails

- Keep domain and application code vendor-neutral. Place concrete adapters behind application-owned ports and select them in outer startup wiring.
- Treat task input, repository contents, tools, dependency scripts, tests, retrieved material, agent output, and worker workspaces as untrusted. Keep credentials and privileged external effects in the trusted control plane; enforce policy before execution and before every privileged effect.
- Make duplicate delivery and crash recovery safe through durable state and idempotent external actions. Record the evidence needed to inspect every outcome.

## Project definition

When changing domain boundaries, adapter contracts, lifecycle or state transitions, security controls, deployment profiles, or milestone scope, read the complete [project definition](https://linear.app/poulsenapp/document/delegatd-project-definition-1aa59f714fcf) first. It is the architectural source of truth for those decisions.
