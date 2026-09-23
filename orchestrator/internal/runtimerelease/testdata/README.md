# Pinned Pi RPC patch fixture

`pi-rpc-0.77.0.js` is the unchanged distributed RPC module from
`@earendil-works/pi-coding-agent` 0.77.0. Its SHA-256 is
`d0f92418f6aedaa4c64da68f65cb6f93e18dc65d13969eba45349d99f36dfca7`.
The adjacent [MIT license](PI-LICENSE) is from upstream tag `v0.77.0`.

This fixture tests the exact input/output pins of the native settlement extension.
The image builder patches its installed copy and rejects any other input; this
file is not installed into the guest. The production patch is defined in
[native_rpc.go](../native_rpc.go).
