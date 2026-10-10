# Queued-prompts release reference

The queued-prompts model separates saved input intent, execution Runs, native
input delivery and durable mutation receipts. Current release requirements and
activation procedures are maintained in one place:

- [Current contract](compatibility.md): exact schema, runtime and protocol identity.
- [Delivery](delivery.md): immutable candidates and executable qualification.
- [Operations](operations.md): guarded updates within the current contract.
- [Bootstrap](bootstrap.md): deliberately scoped fresh installations.
- [Fresh deployment](fresh-deployment.md): latest measured development deployment.

The [2026-09-25 acceptance record](queued-prompts-acceptance.md) describes that
historical transition only. It does not qualify newer artifacts or provide a
migration procedure.
