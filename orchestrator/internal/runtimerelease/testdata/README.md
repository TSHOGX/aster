# Third-party fixture notice

`pi-rpc-0.77.0.js` is the unchanged distributed RPC module from
`@earendil-works/pi-coding-agent` 0.77.0. Its SHA-256 is
`d0f92418f6aedaa4c64da68f65cb6f93e18dc65d13969eba45349d99f36dfca7`.
The adjacent [MIT license](PI-LICENSE) is from upstream tag `v0.77.0`.

The `pi-session-0.77.0.js`, `pi-session-0.77.0.d.ts`, `pi-index-0.77.0.js`
and `pi-index-0.77.0.d.ts` files are unchanged distributed session and export
modules from the same package. They are pinned build-patch inputs, not application
implementations. Tests execute the resulting v1 modules; the runtime image contains
no session migration functions or migration exports. The same MIT license applies.
