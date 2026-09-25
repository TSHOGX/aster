# Queued-prompts acceptance

Current release: schema 3, native protocol 4. The authorized reset, rebuild and
restart completed on 2026-09-25. The [current deployment record](../dev/measurements/current-deployment.json)
binds executable, runtime, schema and frontend identities to the verification
below. Evidence for retired builds and old schemas has been deleted.

| Contract | Current verification |
| --- | --- |
| Input claim and receipts | Go transaction and race tests: one claim path, exact target, same-ID retry, handled/consumed exclusivity |
| Native lifecycle | Pinned real AgentSession and RPC tests: duplicate templates, tool batch, handled input, observer failure, core cleanup failure |
| Run settlement | Real Khora release suite: retry/compaction, Steer consumption, Stop removal, next-run non-leak, gateway continuity and owner handoff |
| Projection | Current-source tests: stable input anchors, page/delta/rebuild order, unknown clocks and late-tool ownership |
| Browser | Production frontend through real API: Files/PTY, Chinese navigation, schedules and two-tab queued Steer handoff |
| Deployment | Empty new database and engine store, matching installed artifacts, seven active supervisors, zero restarts |
| Live smoke | Temporary login, workspace before/after compute, actual provider completion, deployed browser editing, temporary-user/Project cleanup |

`go test ./...`, product/runner/runtime race suites, frontend typecheck, lint,
206 unit tests and production build passed. One frontend test remains opt-in.
The first browser run hit a terminal attach timeout and also ran the single-run
Steer case with a two-request provider gate. The fixture selection was corrected;
the complete isolated browser rerun passed. No test timeout was relaxed.

Live smoke used the installed artifacts, followed by a second empty
initialization after confirming no user work existed. Final business tables are
empty, SQLite integrity is healthy, foreign keys have no errors and credentials
are preserved. The reset does not retain smoke tombstones.

Physical assistive-device testing and arbitrary third-party extensions are not
covered exhaustively. Running clocks are provisional; settled durations use
native evidence. The current run did not repeat the sustained combined workload
or every composed crash permutation, so old performance measurements are not
presented as current-release results.
