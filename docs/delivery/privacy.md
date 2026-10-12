# Public reference and private inputs

Public source contains generic configuration examples, architecture, contracts,
reproducible tests and build tooling. It must not contain real credentials,
endpoint/account inventories, user databases, native histories, deployment paths,
operational logs or private planning/evidence links. Installation reports,
deployment histories, machine-specific acceptance results and artifact inventories
are excluded even when sanitized. Public docs describe reusable procedures and
verification methods, not the state or history of a particular installation.

Keep deployment configuration and raw evidence outside source control. The local
`private/` directory is ignored and must never be force-added, shipped in an image
or included in a CI artifact. Reviewed shared assets must be deliberately selected
and checked before publication; a hash does not remove private content.

`python3 scripts/check-public-docs.py` checks reference language, local links,
account-specific home paths and the tracked/private boundary. It supplements
manual content review and is not a complete secret/history audit.
