# Agent drivers

Pi (`pi`, product mode `agent`) is the only Agent driver. It runs through Khora's
process API and the versioned `RunTurn` bridge protocol. Enabled drivers come from
validated launch plans and the pinned template manifest; a manifest that declares
any other driver is refused.

A shell is not a driver. Terminals are a Project capability with their own native
PTY processes, described in [the Project model](project-model.md); terminal bytes
never become conversation messages or turns.

Pi uses its native RPC protocol and pinned package identity from `internal/agents`.
Native history remains in `/root/.pi/agent/sessions`; completed turn state advances only
after a matching digest/size snapshot and fenced product commit. The launcher restores
the selected native history on a cold process start. Interrupt/steer control is bound
to a generation and turn, durably acknowledged, and reconciled after uncertain delivery.

Drivers do not select execution backends, perform host UID translation or carry real
upstream credentials. Shared context and skills come from a versioned read-only
`/root/.agents` volume through Pi's native discovery paths.
