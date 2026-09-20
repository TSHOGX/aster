# Example authored agent configuration

Copy this tree, set the gateway origin and models in `pi/models.json`, select
`defaultProvider` and `defaultModel` in `pi/settings.json`, and
point a plan's `config` at the copy. The daemon reads the tree, computes its
revision digest and publishes it to an immutable volume mounted read-only at
`/harness-config`; a launch refuses rather than mounting a revision this engine
store has not published.

`apiKey` stays the non-secret `harness-placeholder`. Upstream credentials belong
to the gateway process, never to guest-readable configuration. Keep real origins,
model catalogs and deployment inventories out of version control.

The bridge uses these native settings for Pi and its local readiness probe. There
is no separate launch environment override for model or origin.
