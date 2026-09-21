# Files and native runtime

The released image contains pinned sandboxd, `agent-runner`, `workspace-files`,
`runtime-cleanup`, `runtime-verify`, Node and native Pi. Python and scientific
packages are workload tools. Application launch, journal replay, Files and
cleanup protocols are implemented in Go.

Pi runs in RPC mode with a stable native Session file under
`/runtime/sessions/<session>/native`. Each Session initially selects models and
settings from the Project's immutable `/opt/agent-config/pi` mount. Its original
provider catalog survives later runner incarnations. The application injects an
incarnation service grant where a configured provider expects a service token;
upstream credentials never enter that catalog.

The builtin system context is `/usr/local/share/project-runtime/system.md`.
Reviewed `/opt/shared-assets/AGENTS.md` and optional
`/opt/agent-config/pi/APPEND_SYSTEM.md` append context. Explicit skills come from
`/opt/shared-assets/skills`. Automatic extensions, skills, prompt templates and
themes are disabled; the explicit reviewed skills still load through Pi's native
CLI. Context directories are logical organization, not isolation between sibling
Project processes.

The Files helper confines all operations to `/workspace`. It walks components
with `O_PATH|O_NOFOLLOW` and verifies each selected object with `fstat`; ordinary
`O_RDONLY|O_DIRECTORY|O_NOFOLLOW` alone is insufficient in the pinned runsc.
Regular content is opened through the selected descriptor. Symlinks, special
files, absolute paths, traversal and private runtime access are rejected.

Reads/writes stream with a 256 MiB transfer bound. Writes require length and
SHA-256 and replace atomically after verification. Directory pages contain at
most 200 entries, with a 100,000-entry scan bound. Watchers have bounded lifetimes
and trigger authorized rescans. Cancellation closes the guest stream and releases
its reservation. Unknown mutation outcomes are not retried automatically.

Native journal records and user-visible projections are separate. Completion
creates a synced immutable native capture before the next prompt can replace its
source. Host exports retain native history for inspection and recovery; rendered
conversation history does not substitute for the native Session.
