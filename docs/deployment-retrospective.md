# Deployment Retrospective: Local OpenAI-Compatible Model + Database Skill

This document records deployment lessons from bringing BatteryGPT Agent Runtime Platform onto a Linux host, connecting a local OpenAI-compatible LLM, and adding a read-only PostgreSQL query skill. All hostnames, IPs, credentials, and internal mirror URLs are omitted or replaced with placeholders.

## Architecture Reminder

```
Workbench (Next.js)
  → Orchestrator (Go) :8090
    → gVisor sandbox + Agent (Pi)
      → Anthropic Messages API
        → model proxy (host-only) :8083
          → OpenAI-compatible upstream (e.g. vLLM) :8094
```

Credentials and upstream URLs stay on the host. The sandbox only sees the Anthropic-shaped proxy endpoint injected by the orchestrator.

## What Worked

1. **Linux host required** — gVisor / `runsc` is not available on macOS; deploy on Ubuntu (or similar) Linux.
2. **Cross-compile orchestrator on Mac** when the server cannot fetch Go modules (corporate TLS MITM):
   ```bash
   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o orchestrator ./cmd/orchestrator
   ```
3. **Install runsc from the official gVisor tarball** when apt sources are blocked.
4. **Rootfs build offline-friendly**:
   - Prefer reachable apt mirrors; if DNS inside chroot fails, use mirror IPs or host-side package download + offline install.
   - Install Node.js + Pi Agent on the host, then copy into the rootfs; do not rely on `npm` inside the chroot when DNS/TLS is broken.
5. **Frontend**: if port 8000 is taken, use another port (e.g. `8001`) and set `HARNESS_DEV_ORIGINS` / CORS accordingly.
6. **Model path**: use an Anthropic↔OpenAI bridge (e.g. `claude-code-proxy`) in front of vLLM / OpenAI-compatible servers. LiteLLM gateway streaming had tool-input delta bugs with some models; the dedicated proxy was more reliable.
7. **Orchestrator health probe**: the proxy must expose **`GET /healthz` returning 200**. A `/health`-only endpoint is not enough — the orchestrator treats non-200 as unhealthy and will not inject credentials into the sandbox.
8. **Database skill**: for PostgreSQL, use `psycopg2` (or `psql`) with a **read-only** role. Prefer a skill under `/agents/skills/<name>/SKILL.md` so new sessions pick it up via Agent Bridge.

## Failure Modes (Scrubbed)

| Symptom | Likely cause | Fix direction |
|--------|----------------|---------------|
| `runsc` missing | apt blocked | Official gVisor release tarball → `/usr/local/bin` |
| Rootfs debootstrap hangs | Mirror/DNS unreachable in chroot | Reachable mirror; host-side downloads; real upstream DNS (not only stub resolver) |
| Go module download fails | TLS interception / no proxy for `proxy.golang.org` | Cross-compile elsewhere; or configure trusted MITM CA + GOPROXY |
| Frontend npm hang | Registry unreachable | Copy `node_modules` from a machine that can install |
| UI “CORS” / blank API | Wrong origin allowlist or wrong API port | Align `HARNESS_DEV_ORIGINS` with the browser origin |
| Chat never replies | Proxy down, wrong `/healthz`, or idle session after failed inject | Ensure proxy + `/healthz`; start a **new** session after fixing credentials |
| Proxy starts then dies under sudo | Wrong `uv` / venv path as root | Run as the service user with project `.venv` |
| Matplotlib hangs in sandbox | Waiting for GUI backend | `MPLBACKEND=Agg` / `matplotlib.use("Agg")`; never `plt.show()` |
| DB skill points at wrong engine | Port 5432 ≠ MySQL/Doris | Match driver to actual server (Postgres → `psycopg2`) |
| Agent cannot reach DB IP | Sandbox egress / DNS policy | Allowlist host+port in network policy; verify with `getent` + TCP probe inside session |

## Model Proxy Checklist

Environment (names only; values are secrets — keep out of git):

```bash
export ANTHROPIC_API_KEY="<shared-token>"   # must match what orchestrator injects
export OPENAI_API_KEY="<upstream-token-or-dummy>"
export OPENAI_BASE_URL="http://127.0.0.1:<upstream-port>/v1"
export BIG_MODEL="<served-model-name>"
export MIDDLE_MODEL="<served-model-name>"
export SMALL_MODEL="<served-model-name>"
export HOST="127.0.0.1"
export PORT="8083"
```

Harness / orchestrator side:

```bash
export HARNESS_ANTHROPIC_BASE_URL="http://127.0.0.1:8083"
export HARNESS_ANTHROPIC_AUTH_TOKEN="<same-as-ANTHROPIC_API_KEY>"
# Prefer leaving HARNESS_ANTHROPIC_API_KEY unset if using AUTH_TOKEN only,
# or set both to the same value — mismatch causes 401 inside the sandbox.
```

Verify before UI testing:

```bash
curl -sS -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8083/healthz   # expect 200
curl -sS http://127.0.0.1:<upstream>/v1/models | head
# Anthropic-shaped non-stream and stream probes against :8083
```

If `/healthz` is missing on the proxy, add an alias to the existing `/health` handler.

## PostgreSQL Skill Pattern

Do **not** commit real hosts, users, or passwords. Seed template:

`sandbox-image/files/usr/local/share/agents-kb-seed/skills/postgres-query/SKILL.md.example`

On the server only, install:

`/var/lib/harness/agents-kb/current/skills/postgres-query/SKILL.md`

with placeholders filled:

- `<PG_HOST>`, `<PG_PORT>`, `<PG_DATABASE>`, `<PG_RO_USER>`, `<PG_RO_PASSWORD>`

Guidelines for the skill text:

- Read-only role; `SELECT` / metadata only; row `LIMIT`.
- Prefer parameterized SQL; never log passwords.
- Document that headless plotting needs Agg backend.
- After editing KB files, start a **new** Agent session so Pi reloads skills.

Optional: remove or leave unconfigured any Doris/MySQL skill templates that do not match the real database.

## Suggested Bring-Up Order

1. `runsc` + rootfs (`make sandbox-rootfs` or equivalent).
2. Orchestrator binary + data dir (`HARNESS_DATA_DIR`).
3. Model upstream healthy → proxy with `/healthz` → harness env pointing at proxy.
4. Frontend with correct `PORT` and CORS origins.
5. Smoke: new Agent session → short text round-trip → optional artifact/plot.
6. Install DB skill on host KB path → new session → read-only probe query.

## Security Notes

- Never commit API keys, DB passwords, internal IPs, or corporate mirror hostnames.
- Keep model credentials on the host; sandboxes receive short-lived inject files only.
- Use a dedicated read-only DB user for agent queries.
- Restrict sandbox egress to required destinations (proxy, DNS, approved DB endpoints).

## Related Docs

- [README](../README.md)
- [Architecture](./architecture.md)
- [Operators](./operators.md)
- [Skills](./skills.md)
- [Model credentials (design)](./model-credentials.md)
