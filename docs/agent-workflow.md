# Autonomous and parallel agent workflow

This workflow is client-neutral. Codex, Claude, Cursor, Antigravity, OpenCode, and human
contributors follow the same repository and evidence contracts.

## Durable context

- Public truth: `AGENTS.md`, ADRs, contracts, tests, changelog, and roadmap.
- Local coordination: ignored `kanban/` and immutable `.codex/handoffs/`.
- Structural context: ignored `.code-review-graph/`, refreshed before impact-sensitive review.
- Runtime evidence: ignored artifacts, screenshots, transcripts, and state until sanitized.

Do not add a second semantic-memory system by default. Tracked documentation is reviewable and
versioned; local graph state is reproducible. Persistent free-form agent memories can become stale,
duplicate decisions, and accidentally retain private environment data.

## One task, one owner, one worktree

1. A coordinator creates or selects a task and records the done condition, dependencies, owned
   paths, and verification evidence.
2. Claim the task before editing. Parallel tasks must have non-overlapping primary ownership.
3. Create a dedicated branch and Git worktree outside the main checkout, for example:

   ```sh
   git worktree add ../agent-machine-control-worktrees/13-quality \
     -b agent/13-quality main
   ```

4. Do not edit another task's owned files. Shared manifests, public APIs, or generated files belong
   to an explicit integrator lane.
5. Commit coherent checkpoints. A handoff names the branch, commit, checks run, known gaps, and exact
   next action.
6. Remove disposable worktrees only after committed handoff or merge and after checking dirty state
   and live process working directories.

For several worktrees, the coordinator owns the canonical ignored Kanban board. Workers receive a
task brief; they do not maintain divergent copies of the board as competing truth.

## Agent implementation loop

1. Read the nearest guidance and claimed task.
2. Use code-review-graph for blast radius or architectural lookup, then verify relevant source.
3. Lock behavior with tests before cleanup or risky refactoring.
4. Implement the smallest complete slice through existing owner modules.
5. Run `make quick` during iteration and `make quality` before review.
6. Refresh the graph and inspect `make graph-review` before handoff.
7. Request independent code/security and architecture reviews.
8. Address findings, rerun evidence, and hand off one immutable commit.

## Parallel safety

- Never share an uncommitted checkout between writers.
- Never infer ownership from process presence or task text; use the coordinator's assignment.
- Avoid two agents editing dependency files, schemas, workflows, or the same package concurrently.
- Reviewers are read-only. Integrators own conflict resolution and final verification.
- A completed command, agent turn, or green CI job is evidence, not acceptance by itself.

## Graph use

The repository exposes pinned code-review-graph MCP configuration through `.mcp.json`.

```sh
make graph-build
make graph-update
make graph-review
```

Graph output narrows which files, callers, flows, and tests to inspect. It can be stale or incomplete,
so source and tests remain authoritative. Embeddings and cloud enrichment are disabled by default.
