# Performance contract

Performance follows bounded work and explicit ownership. Measurements from
retired builds are not claims about the current release. Reproduce observations
with the commands in [verification](verification.md) and retain only the current
candidate report.

## Conversation and controls

The server projects durable native records in bounded batches; HTTP reads never
reconstruct whole native logs. Indexed display positions order pages, deltas and
navigation. Projection generations invalidate stale cursors after rebuild.
The browser retains bounded contiguous windows and advances its source frontier
only after applying the corresponding data. Head progress alone does not hide a
consumed synchronization row.

Pending inputs live in a paginated FIFO queue. Hold skips automatic dispatch;
Release retains original enqueue priority. A prompt-local mutation cannot block
urgent Stop. Each queue row has its own in-flight lock. Submission and Stop have
separate request identities and preserve drafts across uncertain replies.

Runtime occurrence timestamps and monotonic elapsed values drive settled clocks.
Live clocks are provisional wall-clock estimates. Unknown execution stops live
clock updates; missing terminal evidence never becomes an invented duration.

## Resource bounds

The writer reserves independent Stop, control, ingestion and background capacity.
Count and byte reservations bound queued work. Native journal, output window and
control reserves apply backpressure while preserving original command identities.
ACK follows committed host ingestion; compaction never discards unresolved effects.

The workbench bounds conversation windows, directory pages and decoded previews.
Large files use capped captures and explicit downloads. Cold workspace reads use
volume APIs without activating compute. A warmed Project reader remains reusable
without turning each read into a mutation ledger.

## Measurement

Use `go test` scale and pressure tests for indexed query and queue behavior,
Playwright for browser interactions, and the isolated `mixed`/`combined` Khora
suites for sustained concurrent work. Report sample count, warm-up, quantile,
CPU/network conditions and build identity alongside timings. Synthetic provider
streams establish control-path behavior, not external provider latency. Do not
reuse metrics from a different schema, native patch or frontend build.
