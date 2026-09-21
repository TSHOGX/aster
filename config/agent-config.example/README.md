# Authored Agent configuration

Copy this tree and select a reachable service origin/model catalog in
`pi/models.json` and native defaults in `pi/settings.json`. Point application
`content.agent_config` at the reviewed copy. Publication creates an immutable
read-only volume mounted at `/opt/agent-config`; each Session selects its native
configuration from `/opt/agent-config/pi` on first launch.

`apiKey` remains the non-secret `project-service-token`, which the Agent runner
replaces with its scoped application grant. Upstream credentials belong to the
host gateway. Keep real endpoints/model inventories out of version control.
Optional `pi/APPEND_SYSTEM.md` adds reviewed native context. Shared skills belong
in the separately published shared-assets tree.
