# Prompt/run lifecycle dependency inventory

Source baseline: `ca4e587`. The table below is the historical P1 change inventory for the prompt/run split in the queued-prompts specification. The current disposition appears after the table.

| Current owner or consumer | Current dependencies | Required destination |
| --- | --- | --- |
| Schema authority | `schema.sql`: `turns` state check, `turns_one_active`, `turns_pending`, `release_turn_pending`, `owner_transferable_turns`, release occupancy view, `notify_turn_outcome`, `release_owner_session_changed`, `release_owner_turn_finished`, `release_ready_turn_*`, `release_turn_dispatch`, `release_turn_insert`, `turn_control_update` | A prompt row owns queue policy and claim/disposition. A run row owns active/terminal/unknown execution. Delivery rows own unresolved native inputs. Rebuild each partial index, view and trigger against the new owners. |
| Admission and queue claim | `turns.go` admits and releases by `held`/`queued`; `queued_controls.go` holds/cancels; `agents.go` checks active states, selects first queued by sequence, claims `starting` | Keep FIFO policy on prompts; give normal dispatch and explicit Steer one atomic delivery-claim authority. A claimed input cannot be selected again. |
| Native command reducer | `agents.go` maps prompt command progress onto `starting`/`running`/terminal turn states and marks a steer source `completed` on ACK | Create/settle the run from the normal prompt command. ACK affects delivery receipt only; correlated consumption changes input disposition. |
| Stop and Steer | `agent_controls.go` finds active turn, checks held source, parks queued rows, sets `stopping`; `agent_service.go` sends controls and handles unknown transport | Target one run ID and incarnation fence; persist mutation receipt and delivery. Stop closes admission to that run while retaining complete native settlement. |
| Recovery and cleanup | `agent_recovery.go`, `agent_service.go`, `placement_children.go`, `children.go`, `child_retirement.go`, `agent_idle.go`, `workers.go` count or mutate running/unknown/queued turns | Run and unresolved delivery state must block duplicate execution, owner handoff, idle retirement and deletion; only proven undelivered inputs return held. |
| Release and activity | `schema.sql` release views/triggers; `activity.go`, `retirement.go`, `projects.go`, `observation.go` count or park/cancel turns | Include unresolved deliveries and runs in drain/occupancy; cancellation affects unclaimed prompts only. |
| Service authority | `network.go`, `network_transition.go`, `gateway_requests.go`, `schema.sql` owner transfer view and grant triggers | Authorize against exact active run/owner, including its stopping interval; do not infer active execution from prompt policy. |
| Conversation projection | `display_projector.go`, `display_queries.go`, `display_content.go`, `turn_summary.go`, `turn_index.go`, `adjacent.go` use turn state/sequence for synthetic input, order, summaries, title and navigation | Project only consumed input anchors; retain run identity separately from segments. Order by durable source position, not enqueue sequence. |
| Schedules and shares | `schedules.go` exposes `t.state` as scheduled run state; `shares.go`, `publications.go`, `captures.go` reference turn/command outcome | Map scheduled work to prompt intent and its run outcome explicitly. Preserve share/capture evidence through the new IDs. |

The SQL schema has two distinct partial indexes for active/unknown turns (`turns_one_active`, `release_turn_pending`) and a release view that uses the latter with `INDEXED BY`. Moving only the unique constraint would leave release readiness incorrect. `agent_idle.go` and `workers.go` independently treat queued prompts as occupancy, while the owner transfer view requires an applied normal prompt command and a running turn. These are cross-cutting gates, not display-only uses.

The grep below is a useful drift check during implementation, but is not by itself a completion proof because local `Turn.State` comparisons and SQL spread across concatenated literals also exist:

```sh
rg -n --glob '*.go' --glob '*.sql' --glob '!**/*_test.go' 'turns.*state|t\.state|turnColumns' orchestrator/internal/product
```

G2 remains open until the new schema, DTOs, all listed guards, and transaction tests replace these dependencies. This inventory is a review checklist for that coordinated change.

## Authority audit after the foundation correction

The inventory above records the earlier baseline. Current prompt admission/mutation reads `queue_policy` and `disposition`; `createDelivery` is the common claim transaction. Runs own execution guards in `agents`, `agent_controls`, `agent_service`, `agent_recovery`, `placement_children`, `children`, `workers`, `agent_idle`, `network`, and schema release/owner views and triggers. `turns.acknowledged` is removed. `activity` combines pending released prompt occupancy with live run occupancy. Cleanup now deletes receipts/deliveries/runs and derived segment tables in dependency order. Captures permit multiple run paths for a retried prompt.

Remaining `turns.state` reads are compatibility display/status consumers (head/summary/schedule/title/history), not execution admission. Its direct normal-dispatch assignment is a display cache update after the transactional claim. New control DTOs use prompt revision and exact run/incarnation/owner; the session-revision Go wrappers remain internal adapters. See [the current handoff](../private/queued-prompts-analysis-20260924/07-implementation-handoff.md) for the explicit remaining native qualification, client-frontier and format-transition prerequisites.

The session-revision Go wrappers mentioned above were removed in the exact-control cleanup stage. Test calls now use the prompt revision or exact run fence directly. The `turns.state` display cache and its remaining consumers are still pending removal.

The derived-status stage removed `turns.state` from the fresh schema. `turns.go` derives the public prompt status from policy, disposition and correlated runs. Run transitions own terminal notifications and active display status; prompt changes own queue refresh revisions. Schedule, activity, title, summary and navigation queries no longer read the former column. Tests that previously wrote turn state now use run or explicit segment evidence. The earlier rows remain as an audit of the dependencies that were replaced, not as a description of the current schema.

Queue reads now compare an optional client-applied projection frontier with each consumed delivery's durable source position. This keeps the synchronization row visible to a client whose page/delta has not caught up, independently of the server projector's anchor table.
