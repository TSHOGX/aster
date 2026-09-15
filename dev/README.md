# Local dev (microVM) alongside the ECS deployment

Runs the whole platform — orchestrator, model proxy, frontend, and real gVisor
sandboxes — on a local Linux machine that has `runsc`, reachable from a browser
on the host. Verified in an OrbStack Ubuntu VM on Apple silicon (arm64); the
same scripts work on other Linux hosts.

The repo's root `AGENTS.md` says there is no local runtime. That is about the
macOS host, not this VM.

## Portable vs per-machine

| Kind | What | Where |
|---|---|---|
| **Portable (commit / share)** | start/stop scripts, rootfs build, DNS-pin hook, model-proxy code, setup installer | `dev/start-all.sh`, `dev/stop-all.sh`, `dev/setup-linux.sh`, `dev/configure-local.sh`, `dev/build-dns-pin.sh`, `sandbox-image/build-rootfs.sh`, `dev/model-proxy/proxy.py`, `*.example` |
| **Per-machine (never commit)** | API keys, optional mirrors, optional origin overrides, build outputs | `dev/model-proxy/.env`, `dev/local.env`, `sandbox-image/build.env`, `dev/logs/`, `dev/orchestrator`, `sandbox-image/rootfs/` |
| **Auto-detected** | VM IP, checkout owner, Ubuntu mirror by arch, fake-ip DNS when present | `start-all.sh`, `build-rootfs.sh`, `configure-local.sh` / `build-dns-pin.sh` |

Rule of thumb (same as below): committed files must behave correctly on every
machine without editing. Anything true of only one host goes in a gitignored
file or is auto-detected.

## Quick start on a new Linux host

```bash
# Inside the Linux VM / host, from a checkout on local disk
# (prefer a native path; OrbStack shared Mac mounts can hang under NFS)
./dev/setup-linux.sh

# Fill the only required secret:
$EDITOR dev/model-proxy/.env   # set UPSTREAM_API_KEY=...

sudo SANDBOX_AGENT_DRIVERS=pi,sh ./sandbox-image/build-rootfs.sh
sudo ./dev/start-all.sh
```

`setup-linux.sh` installs packages / Go / Node / `runsc` / frontend deps, then
runs `configure-local.sh` to seed gitignored config from examples. Re-seed
config only with:

```bash
./dev/setup-linux.sh --configure-only
# or
./dev/configure-local.sh          # keep existing files
./dev/configure-local.sh --force  # overwrite from examples
```

If the host sits behind a fake-ip proxy (clash and similar), `configure-local.sh`
detects it and sets `SANDBOX_BUILD_DNS_HOOK` in `sandbox-image/build.env`
automatically. On a normal resolver it leaves the hook commented.

## How local and ECS stay in sync

The two environments genuinely differ (arch, DNS, TLS, addresses, who runs the
proxy). The rule that keeps the repo shared anyway:

> **Committed files must behave correctly in both environments without editing.**
> Anything true of only one machine goes in a gitignored file or is auto-detected.

So there is no local-vs-ECS branch to reconcile, and no diff to carry. Three
mechanisms, in order of preference:

1. **Auto-detect** when the environment can be observed. `build-rootfs.sh` picks
   `ports.ubuntu.com` vs `archive.ubuntu.com` from the host architecture.
   `dev/start-all.sh` detects the VM IP and the checkout's owner.
2. **Derive from a signal that already exists.** The CSP adds `'unsafe-eval'` only
   when `NODE_ENV=development`, because `next dev` needs it and production does
   not. The deployed policy is byte-identical to before (verified against
   `next build` + `next start`).
3. **Read from a gitignored file** when neither applies: `dev/local.env`
   (see `dev/local.env.example`) for paths/IPs/ports, `sandbox-image/build.env`
   (see `build.env.example`) for package mirrors and the DNS baked into the image,
   `dev/model-proxy/.env` for the upstream key. All are covered by `.gitignore`.

Mechanism 3 is the one to reach for when the value belongs to a *deployment* — a
mirror, a resolver, a public IP. Committed defaults are upstream-canonical and name
no environment, so neither machine's setup rides along into the other's build.

What this means in practice: commit normally. If a change would only be right on
one machine, that is the signal to convert it into one of the three forms above
rather than to keep it uncommitted. Secrets and build outputs stay out of git —
`dev/logs/`, `dev/orchestrator`, `dev/local.env`, and `**/*.env` are ignored.

## Start / stop

```bash
# Start model proxy (:8083) and MCP gateway (:8103) yourself first.
sudo ./dev/start-all.sh     # orchestrator :8090 + frontend :8000
sudo ./dev/stop-all.sh      # does not kill :8083 / :8103
# sudo START_PROXY=1 ./dev/start-all.sh   # also launch the stand-in proxy
# sudo KILL_PROXY=1 KILL_MCP=1 ./dev/stop-all.sh
tail -f dev/logs/*.log
```

`start-all.sh` prints the URL to open. The orchestrator runs as root (it manages
netns, nft, runsc, and `/var/lib/harness*`). Both scripts use `setsid`: a plain
backgrounded `nohup` dies with the launching shell when invoked through
`orb bash -lc` or a non-interactive ssh.

This checkout's long-lived branch is **`upstream-work`** (latest `main` plus the
dingpan MCP overlay and `executeQuery` file landing). Do not open a new feature
branch for each follow-up; keep working here and merge `origin/main` into it.

Ports stay: orchestrator **:8090**, frontend **:8000**, model proxy **:8083**,
MCP gateway **:8103**. The last two are external processes on this branch.

Reach it from the host browser at the VM's printed IP (for example
`http://<vm-ip>:8000`). OrbStack `.orb.local` names are not reliable for this.
Login is open mode: with no `config/auth-users.yaml` the workbench skips the
login screen entirely and everything belongs to the `lab` user.
`HARNESS_COOKIE_INSECURE=1` is set because this is plain http. Add
`config/auth-users.yaml` to get the real login gate (see the root README) —
`orchestrator user add <name> --password-stdin` writes it for you, and the
running backend picks it up without a restart.

## Per-machine secrets (fill once)

| File | Required fields | Notes |
|---|---|---|
| `dev/model-proxy/.env` | `UPSTREAM_API_KEY` | Copied from `.env.example` by configure |
| `sandbox-image/build.env` | usually none | Mirrors / VPC DNS / DNS hook when needed |
| `dev/local.env` | usually none | Override origins / frontend user if auto-detect is wrong |
| `config/title-llm.yaml` | optional | Session-title LLM; gitignored if you add secrets |

Rebuild the rootfs after changing the sandbox image:

```bash
sudo SANDBOX_AGENT_DRIVERS=pi,sh FORCE=1 ./sandbox-image/build-rootfs.sh
```

## The model proxy

`claude-code-proxy` is not in this repo (ECS deploys it separately), so local dev
uses a stand-in at `dev/model-proxy/proxy.py`. It does the two things the platform
requires: authorize every request against the orchestrator's correlation Unix
socket (`start` → forward → `finish`), then forward upstream with the real API key
injected. The sandbox only ever holds a dummy key — do not "simplify" this by
letting Pi reach upstream directly, that is the credential boundary.

It targets ModelArts' **native Anthropic endpoint**
(`https://api.modelarts-maas.com/anthropic`), so it is a straight pass-through with
no protocol translation. The ECS proxy instead targets `/openai/v1` and translates.
This is a real behavioral difference between the two environments, contained
entirely inside the untracked `.env`.

`HARNESS_PROXY_CORRELATION_SOCKET` must match `harness.yaml`'s `run_dir` —
currently `/var/lib/harness-publish/run/proxy-internal/proxy-correlation.sock`.
Pointing it at the wrong run dir gives `Errno 2` and every turn 503s.

## DNS: the thing that breaks everything locally

If the host runs a fake-ip proxy (clash and similar), **every** hostname resolves
inside the VM to an unroutable `198.18.0.0/15` address — including when querying
1.1.1.1 directly, because the interception is upstream of the VM. Changing
nameservers does nothing. The VM works around it with pinned IPs in its own
`/etc/hosts`, and `dev/build-dns-pin.sh` does the same for the build chroot —
wired in via `SANDBOX_BUILD_DNS_HOOK` in `sandbox-image/build.env` (or
auto-wired by `configure-local.sh`). Extend `PIN_HOSTS` / `BUILD_DNS_PIN_HOSTS`
when a build stage starts downloading from a new host.

Symptoms, each of which looks like an unrelated network flake:

| Symptom | Cause / fix |
|---|---|
| `build-rootfs.sh`: hundreds of `Failed to fetch ... [IP: 198.18.0.39]` | chroot DNS unusable — run `./dev/configure-local.sh` or set `SANDBOX_BUILD_DNS_HOOK` |
| model proxy: `SSLEOFError` on every upstream call | pin the upstream API host in the VM's `/etc/hosts` |
| `npm install`: `ECONNRESET` from a mirror host | lockfile `resolved` URLs were committed from a host with an npm mirror configured. `scripts/check-lockfile-registry.sh` now fails CI on this; keep mirrors in `~/.npmrc`, not the lockfile |
| `npm install`: `ENOTEMPTY` / "Exit handler never called" | a previous failed install left `node_modules` corrupt — `rm -rf node_modules` and retry |
| blank page, `Uncaught EvalError` in console | CSP was missing `'unsafe-eval'` for `next dev`; fixed in `next.config.mjs` |

To pin a newly blocked host, resolve it over DoH (which bypasses the intercepted
resolver) and add the answer to `/etc/hosts`:

```bash
curl -H 'accept: application/dns-json' \
  'https://1.1.1.1/dns-query?name=api.example.com&type=A'
```

## Smoke test

```bash
C=/tmp/c.txt
curl -sS -c $C -X POST http://127.0.0.1:8090/api/login -H 'Content-Type: application/json' -d '{}'
SID=$(curl -sS -b $C -X POST http://127.0.0.1:8090/api/sessions \
  -H 'Content-Type: application/json' -d '{"mode":"agent"}' |
  python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
curl -sS -b $C -X POST "http://127.0.0.1:8090/api/sessions/$SID/messages" \
  -H 'Content-Type: application/json' -d '{"content":"Reply with exactly: SMOKE OK"}'
# poll until status is running_idle, then read the messages:
curl -sS -b $C "http://127.0.0.1:8090/api/sessions/$SID/messages"
```

The turn is fully wired only if `dev/logs/model-proxy.log` shows both
`request started ... turn=N` and `request finished ... status=200`, and the
assistant message lands. A `503 active_context_unavailable` between turns is
correct behavior — the sandbox tried to call the model outside its own active turn
and correlation refused.
