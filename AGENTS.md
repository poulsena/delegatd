# delegatd

`delegatd` is a self-hosted Go control plane for defining, running, supervising, and governing configurable agent work.

## Development loop

- Use a dedicated git worktree for every change; keep the primary checkout unchanged.
- Use TDD. Start each implementation change with a failing test at the narrowest public boundary that expresses the observable behavior. Make it pass with the smallest correct change, then run the focused test and the relevant broader checks.
- Before publishing a branch, pull request, or release, follow `CONTRIBUTING.md` for identity, evidence, and gate order.
- Work in vertical slices. Start at a real system boundary and finish at an observable result or safe rejection. Include the schema, policy check, audit record, failure handling, test, and documentation required by that slice.
- Prefer surgical edits over large refactors. Change only what the current slice requires; reserve a broad refactor for cases where the slice cannot be correct without it, and make that necessity explicit.
- Keep tracer bullets small, reviewable, and independently demonstrable. Split by scenario or guarantee, not by horizontal component phase.

## Architecture guardrails

- Keep domain and application code vendor-neutral. Place concrete adapters behind application-owned ports and select them in outer startup wiring.
- Treat task input, repository contents, tools, dependency scripts, tests, retrieved material, agent output, and worker workspaces as untrusted. Keep credentials and privileged external effects in the trusted control plane; enforce policy before execution and before every privileged effect.
- Make duplicate delivery and crash recovery safe through durable state and idempotent external actions. Record the evidence needed to inspect every outcome.

## Project management

Scope, milestones, delivery order, and acceptance criteria are managed in the [Linear project](https://linear.app/poulsenapp/project/delegatd-e331ccb1476e). Establish issue scope before acting by inspecting the current branch for a Linear issue key such as `POU-XX`. A matching key makes the task issue-bound, including commit reviews, even when the request omits the key. For issue-bound work, read the issue's recent comments and the complete [project definition](https://linear.app/poulsenapp/document/delegatd-project-definition-1aa59f714fcf) before acting. Before changing scope, milestone behavior, acceptance criteria, domain boundaries, adapter contracts, lifecycle or state transitions, security controls, or deployment profiles, consult those sources. The project definition is the architectural source of truth; the repository is the source of truth for implementation details, commands, and current behavior.
- Validate recent issue comments against the repository and project definition before acting.

- When working on a Linear issue, keep its assignee and workflow status current. Treat comments as a durable agent message board:
  - Start every comment with `Agent: <harness label> | <kind>`, using `Main` for the coordinator or the assigned worker name/ID for a subagent. The shared MCP account is only the transport author; the harness label identifies the speaker.
  - Use `handoff`, `decision`, `blocker`, `scope`, or `verification` as `<kind>`. Post a comment when another agent needs durable context, and include the current state, evidence, and next action or owner when applicable.
  - Keep routine progress and metadata changes in Linear fields. Reply to the relevant comment when extending or resolving an existing thread.
