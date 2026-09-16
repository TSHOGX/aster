# Development tools

See [development setup](../docs/development.md) for isolated local configuration.
This directory contains a model-proxy adapter and an optional build DNS hook.
Neither tool defines a production host or a default private endpoint.

- `model-proxy/proxy.py`: host-side request forwarding with orchestrator correlation.
- `model-proxy/.env.example`: synthetic environment template; supply real values
  outside version control.
- `build-dns-pin.sh`: optional build hook for constrained resolvers. Configure its
  use explicitly; it is not required by the hosted release pipeline.

Logs, compiled binaries and private environment files are ignored. Host-specific
start/stop wrappers belong in private operations configuration.
