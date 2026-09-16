# Repository scripts

| Script | Purpose |
|---|---|
| `check-lockfile-registry.sh` | Verify portable frontend dependency registry URLs |
| `check-public-docs.py` | Check English documentation, local links and public-file boundaries |
| `harness-kb-nightly.sh` | Optional explicit-config wrapper for memory capture/replay jobs |
| `release/` | Hosted build, manifest, artifact smoke and publication tooling |

Production installation and upgrades use the shipped `harness-deploy` binary;
see [deployment](../docs/delivery/operations.md). User/KB operations use the
orchestrator CLI. Per-host service wrappers and command aliases are private
operations files and are not a supported public release interface.

The memory wrapper is independent of the core user scheduler. It requires an
explicit binary, application YAML, environment file and lock path; it does not
install cron, choose a timezone or discover production paths. See
[memory and knowledge](../docs/agents-kb.md).
