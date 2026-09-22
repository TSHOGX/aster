# Recovery and uncertain effects

Durable intent and evidence are separate. An acknowledged user request keeps its
original identity across retries. The application checks engine store, sandbox,
placement epoch, process incarnation, owner and mutation revision before applying
a result. It does not refresh a fence and redispatch an old mutation.

An application restart reopens the same product database under its exclusive
owner lock. Workers inspect existing placements and process handles, reattach
streams and resume pending lifecycle/capture/retirement work. The native journal
continues beyond the engine's bounded output ring. A fenced read-only command
status query distinguishes absent/received commands from uncertain dispatch.
Only proven absent or received commands can return to pending under the original
command ID and payload. An unresolved command blocks fresh Session dispatch.

The Go guardian outlives its Agent worker. It adopts and terminates escaped tool
descendants before cleanup can be reported. Loss of the guardian leaves cleanup
unknown when surviving descendants cannot be ruled out. A force restart is an
explicit Project operation that establishes placement termination; it is not an
automatic response to a transient connection failure.

Journal replay never starts Pi. Missing or corrupt journals become visible
recovery failures, release the replay activity reservation and support explicit
retry after the cause is repaired. Unknown outcomes remain visible in the turn,
recovery projection and notifications. Captures export independently by Session;
a failed export cannot serialize healthy work behind its retry loop.

PTY reconnect uses the original process incarnation, retained output/end frames
and live process inspection. Engine control-plane process status can lag until
end frames drain. Engine restart can preserve placement and PTYs at the same
epoch; the application does not infer an exit from a transport disconnect.

Files writes and mutations are never replayed after an unknown reply. Inspect
the operation and current file before issuing fresh intent. A streamed read or
watch can reconnect with a fresh authorization check; it conveys no mutation
permission and does not keep the Project awake.

Use operator diagnostics to inspect blockers, pending work and binding mismatch.
Repair dependencies first, then use the explicit retry/restart action appropriate
to the observed failure. Do not open the product database with a second writer or
edit lifecycle rows to make a failed operation appear complete.


A successful runner resynchronization re-ACKs the host's already committed prefix,
including when there are no new records. This recovers capacity after a lost final
ACK. Bounded journal pressure can wait for ACK without blocking the command mutex;
a sustained five-second capacity stall fails explicitly and leaves unresolved
native effects unknown. A telemetry sample is never completion or ACK evidence.
Agent wire 2 and journal format 2 belong to a newly built immutable runtime;
protocol upgrades must follow the qualified admission/drain deployment process.
