# Queued-prompts implementation progress

This record tracks implementation evidence after the planning baseline. It does not amend the frozen screenshot evidence or claim that the acceptance gates have passed.

## P0: full Session identity fixture (2026-09-24)

`orchestrator/internal/runtimerelease/full_session_identity.test.mjs` creates a real pinned Pi 0.77.0 `AgentSession` through `createAgentSession`, with an in-memory session manager, a temporary working directory, no tools, and a deterministic fake model stream. It does not call a provider. The test uses request-local context to tag the exact user message object at the `Agent.prompt` and `Agent.steer` boundaries, and observes those objects through the Session's normal subscription path.

The fixture passes for a normal prompt followed by two identical template-expanded inputs. One input uses `AgentSession.steer`; the other uses `AgentSession.prompt` with `streamingBehavior: 'steer'`. The observed consumption identities are `normal`, `A`, `B` in that order, and the stored user messages contain only the expanded text. This supports exact-message identity as an adapter mechanism even after ordinary Session event handling and persistence. It also shows why text matching cannot identify the two steer sources.

Run it with:

```sh
node --test orchestrator/internal/runtimerelease/full_session_identity.test.mjs
```

G1 remains open. The fixture has no actual RPC stdin/stdout, arbitrary extension transformations or handled commands, retry/compaction continuation, process crash, journal, or target admission gate. It does not prove a removed disposition or close the drained-input race. Those need executable qualification before the P1 wire contract is fixed.

## P1 preparation: lifecycle inventory (2026-09-24)

[queued-prompts-lifecycle-inventory.md](./queued-prompts-lifecycle-inventory.md) maps current `turns.state` ownership across schema, scheduling, recovery, release, service grants, and conversation projection. It is the checklist for replacing the overloaded state with prompt, run, and delivery resources. G2 remains open until that replacement and its transaction tests are complete.
