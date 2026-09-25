# Prompt evidence and presentation architecture

This refinement follows the 2026-09-25 review of `569d7b6`. The five resources
remain: prompt intent, delivery evidence, execution run, immutable request
receipt, and derived conversation segment. There is still one transactional
claim path and one native execution owner.

## Root causes and invariants

Absence of model consumption is not proof of non-execution. An extension may
have handled the input. A presentation observer must not control execution
settlement. A segment boundary is not an execution outcome. Retained historical
input is valid display evidence without being proof of native consumption.

1. Delivery effects are explicit: consumed by the model or handled by an
   extension. Either permanently claims the intent. Only proven removed or
   rejected input can return to held. Unknown remains exclusive.
2. Native settlement closes admission, joins admitted preflights, proves queues
   cleared, emits reconciliation, and releases the run exactly once. Observer
   errors are diagnostics; inability to prove cleanup fences the runtime and
   invokes process recovery without reporting false removal.
3. Segments store input membership, work ownership, final message and native
   timing boundaries. Run state supplies liveness and execution outcome at
   projection time. A continued segment is unaffected by later run loss.
4. Existing item ownership is resolved before any summary side effect. Late
   updates cannot mark a newer segment as worked.
5. Historical input anchors explicitly cite retained source positions. They
   are display provenance, never synthetic native consumption. Conversion
   rejects ambiguous associations. Rebuild preserves both chronology and
   evidence classification.
6. API control errors have typed admission reasons. Queue rows expose durable
   delivery reasons. Browser receipt recovery retains exact intent, consumes
   current result, and isolates row operations, submission and urgent Stop.

## Implementation sequence

- [ ] Native lifecycle adapter and effect contract, schema/reducer invariants,
      handled inputs and listener/cleanup fault tests.
- [ ] Segment boundary model, owner-first item reduction, historical anchor
      normalization, conversion/rebuild and composed regression tests.
- [ ] Typed control API, durable delivery feedback, dedicated browser control
      state, exhaustive segment rendering and interaction tests.
- [ ] Full relevant verification, release/schema/runtime identities and current
      acceptance documentation. Older qualification is historical evidence.

No compatibility dual writes or second scheduler are introduced. The source
version-1 converter remains an offline tool. Schema/runtime changes invalidate
previous candidate identities and require a newly assembled candidate; they
do not authorize live-data changes or deployment.

The code stages are committed as `21d4289`, `c6cfb1b`, `1dc157c` and
`cd298bb`. The fresh schema is version 3 and native protocol 4. Repository Go
and frontend suites pass, including the former review regressions. A new
immutable runtime candidate still needs to be assembled and measured against
the protocol-4 output before release qualification; the older protocol-3
candidate remains historical evidence only.
