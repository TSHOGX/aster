# Agent drivers

Pi (`pi`, product mode `agent`) and shell (`sh`, mode `shell`) run through Khora's
process API and the versioned `RunTurn` bridge protocol. Enabled drivers come from
validated launch plans and the pinned template manifest.

Pi uses its native RPC protocol and pinned package identity from `internal/agents`.
Native history remains in `/root/.pi/agent/sessions`; completed turn state advances only
after a matching digest/size snapshot and fenced product commit. The launcher restores
the selected native history on a cold process start. Interrupt/steer control is bound
to a generation and turn, durably acknowledged, and reconciled after uncertain delivery.

Shell executes queued commands through its guest runner, produces framed output and
supports interruption. A running shell command cannot receive Pi steering.

Drivers do not select execution backends, perform host UID translation or carry real
upstream credentials. Shared context and skills come from a versioned read-only
`/root/.agents` volume through Pi's native discovery paths.
