# Example authored agent configuration

Copy this tree, set the model gateway origin and catalog for your deployment, and
point a plan's `config` at the copy. The daemon reads the tree, computes its
revision digest and publishes it to an immutable volume mounted read-only at
`/harness-config`; a launch refuses rather than mounting a revision this engine
store has not published.

`apiKey` stays the non-secret `harness-placeholder`. Upstream credentials belong
to the gateway process, never to guest-readable configuration. Keep real origins,
model catalogs and deployment inventories out of version control.
