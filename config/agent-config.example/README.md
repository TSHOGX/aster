# Agent configuration example

Operators can copy this tree to a private reviewed location and choose the service
origin/model catalog in `pi/models.json` and defaults in `pi/settings.json`.
Set application `content.agent_config` to that copy, then explicitly publish it
through the running owner. Publication creates a read-only version mounted at
`/opt/agent-config`; each conversation selects its native configuration on first launch.

Keep `apiKey` as the non-secret `project-service-token` placeholder. The runtime
supplies its scoped grant; upstream credentials stay in the host gateway. Do not
commit real endpoints, model inventories or secrets to this example.

Optional `pi/APPEND_SYSTEM.md` adds reviewed context. Put shared skills in the
separately published shared-assets tree. Changing local files does not update a
published version or silently reconfigure existing Projects.

See [gateway configuration](../../docs/operators/configuration.md#model-service-gateways)
and [content publication](../../docs/operators/runtime-inventory.md#publish-reviewed-system-content).
