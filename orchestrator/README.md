# Orchestrator

Go control plane for the sandboxed agent runtime platform. It owns session metadata, starts or restores per-generation gVisor sandboxes, parses agent output, publishes events, and records artifacts written under the session workspace.

## Run

```bash
cd orchestrator
go run ./cmd/orchestrator
```

For a built binary:

```bash
cd orchestrator
mkdir -p bin
go build -o bin/orchestrator ./cmd/orchestrator
./bin/orchestrator
```

## Rebuild / Restart

Use `go run` while iterating locally. When code changes require a full rebuild, rebuild the binary and restart the process:

```bash
cd orchestrator
go build -o bin/orchestrator ./cmd/orchestrator
```

Then stop the current process and start the new binary again:

```bash
./bin/orchestrator
```

If you started it in another terminal, kill that process first and relaunch the rebuilt binary.

Useful environment variables:

- `HARNESS_ORCHESTRATOR_ADDR` defaults to `:8090`.
- `HARNESS_LAB_PASSWORD` enables shared-password auth. Leave empty for local no-auth smoke tests.
- `HARNESS_COOKIE_NAME` defaults to `harness_auth`.
- `HARNESS_SESSION_RETENTION` defaults to `0s`; `0s` disables automatic session expiry.
- `HARNESS_REPO_ROOT` defaults to the repository root.
- `HARNESS_SESSIONS_ROOT` defaults to `/var/lib/harness/sessions`.
- `HARNESS_AGENT_HOMES_ROOT` defaults to `/var/lib/harness/agent-homes`.
- `HARNESS_CHECKPOINTS_ROOT` defaults to `/var/lib/harness/checkpoints`.
- `HARNESS_BUNDLE_ROOT` defaults to `<repo>/bundle/out`.
- `HARNESS_DB_PATH` defaults to `/var/lib/harness/state/orchestrator.db`.
- `HARNESS_DEFAULT_AGENT` overrides `harness.default_agent`; the checked-in
  lab config defaults Agent mode to `pi`.
- `HARNESS_MAX_SESSIONS` defaults to `30` and caps non-terminal sessions, not live `/30` slots.
- `RUNSC_ROOT` defaults to `/var/lib/harness/runsc`.

`HARNESS_SESSION_TTL` is obsolete and fails startup if present; use `HARNESS_SESSION_RETENTION`.

Runtime network and model proxy settings are explicit in `config/harness.yaml`.
The full schema, every knob, and the checked-in values are in
[`../docs/configuration.md`](../docs/configuration.md) — that is the single source
of truth; don't duplicate the block here. Essentials:

- `default_agent` selects the product `Agent` driver (checked-in: `pi`); `sh` is
  reached only through product `mode: "shell"`.
- `model_proxy.bind_url` is the host listener used for probes and egress
  allow-listing; `model_proxy.sandbox_base_url` is the sandbox-visible alias written
  into control manifests.
- `network.egress.allow_all` (checked-in `true`) opens sandbox egress to the whole
  internet; set `false` to lock egress to Doris + the proxy. See
  [`../docs/security.md`](../docs/security.md).
- The loader uses strict YAML decoding: only the top-level `harness:` document is
  decoded, and any unknown top-level or nested key fails startup. Legacy
  `runtime:` / `claude:` / `harness.secrets.*` / `harness.session_ttl` are rejected.

The selected rootfs must contain an image manifest for the drivers selected by
product modes. The checked-in lab default needs Pi Agent and Shell:

```bash
SANDBOX_AGENT_DRIVERS=pi,sh ./sandbox-image/build-rootfs.sh
```

Use `FORCE=1` with the same driver set when the existing rootfs lacks a
selected driver CLI; the non-`FORCE` path only syncs the overlay and regenerates
the manifest. If the manifest is missing `pi`, Agent session
creation/allocation fails before runtime start; if it is missing `sh`, Shell is
hidden or rejected.

With `session_retention: 0s`, retained non-terminal sessions keep counting toward `max_sessions` even after runtime resources are retired. The `/api/quota` response reports the session ceiling and live `/30` pool ceiling separately. Use `DELETE /api/sessions/<id>` to close sessions and free session quota; close preserves messages, artifacts, workspace, and agent-home paths while reclaiming runtime resources.

The runtime launches `runsc` directly in sandbox mode and keeps containers alive
across turns. Each generation gets its own network profile, netns/veth pair,
`/30`, generated runtime spec, control manifest, and file-queue bridge.

`checkpoint.auto_enabled` (checked-in `true`) gates automatic idle *release*: an idle
sandbox is torn down after `idle_threshold` to free its slot, and the next message
cold-starts a fresh generation from the retained workspace + agent-home.
`HARNESS_AUTO_CHECKPOINT_ENABLED` overrides it per new session. See
[`../docs/sandbox-runtime.md`](../docs/sandbox-runtime.md).

## Event Streams

- `GET /api/events` - WebSocket compatibility endpoint
- `GET /api/events/stream?session_id=<id>` - SSE endpoint used by the frontend
- Artifact watcher events include `artifact.updated` for file create/write metadata and `artifact.deleted` for remove/rename cleanup.

## Session Control

- `PATCH /api/sessions/<id>` - rename a session (`{"title":"..."}`)
- `POST /api/sessions/<id>/interrupt` - interrupt a running shell session
- `POST /api/sessions/<id>/artifacts` - multipart upload into the workspace
- `PUT /artifacts/<id>/<path>` - online-edit a workspace text file
- `POST /api/sessions/<id>/messages/<mid>/feedback` - 👍/👎 a message
- `GET /api/search`, `GET /api/notifications` - search and broadcast notifications
- `DELETE /api/sessions/<id>` - close a session, preserve history/workspace state, and reclaim runtime resources

The full route reference and DTO shapes are in [`../docs/api.md`](../docs/api.md).

## Curl Smoke Test

When `HARNESS_LAB_PASSWORD` is set:

```bash
curl -c /tmp/harness.cookies \
  -X POST http://127.0.0.1:8090/api/login \
  -H 'content-type: application/json' \
  -d '{"password":"YOUR_PASSWORD"}'
```

Create a session:

```bash
curl -b /tmp/harness.cookies \
  -X POST http://127.0.0.1:8090/api/sessions \
  -H 'content-type: application/json' \
  -d '{"mode":"agent"}'
```

Use `{"mode":"shell"}` for the PTY shell path when Shell is enabled. Raw
`agent`/`driver_id` input is rejected — the browser only ever picks a product mode.

Send the first message:

```bash
curl -b /tmp/harness.cookies \
  -X POST http://127.0.0.1:8090/api/sessions/<session_id>/messages \
  -H 'content-type: application/json' \
  -d '{"content":"what tables are available?"}'
```

Open the event stream from a browser-friendly client:

```bash
curl --no-buffer \
  http://127.0.0.1:8090/api/events/stream?session_id=<session_id>
```

List artifacts:

```bash
curl -b /tmp/harness.cookies \
  http://127.0.0.1:8090/api/sessions/<session_id>/artifacts
```

Download artifacts:

```bash
curl -b /tmp/harness.cookies \
  http://127.0.0.1:8090/artifacts/<session_id>/<path>
```

Artifact downloads are read-only and limited to regular files under the session workspace. The server rejects traversal, symlink components, symlink escape, directories, and non-regular files.

## Notes

- Product `Agent` resolves to Pi in the checked-in config.
- `sh` is the interactive shell session path; useful for smoke tests and shell-style debugging.
- Checkpoint/restore is generation-aware. Automatic idle *release* is on in the checked-in config (`checkpoint.auto_enabled: true`); true `runsc` checkpoint images are a separate, digest-gated path.
