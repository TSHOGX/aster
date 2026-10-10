# Administration frontend

This independent Next.js build serves `/admin` through the Go browser edge.
Authentication and all product data stay with the Go owner. The workbench login
is shared; non-administrators receive an access-denied view.

Users supports bounded filtering/pagination, creation with default or explicit
memberships, display-name/role edits and one-time password reset. Unknown outcomes
retain the original request for explicit retry; a replay cannot recover a password.
Audit provides bounded actor/target/action/time filters and links to operation
state. Suspension work is reported as accepted until its resource effects are
observed; suspension controls enter the UI with the lifecycle implementation.

```sh
npm ci
npm run build
npm run start
```

Configure the Go application's `admin_frontend` origin (default port 3001).
The base path is compiled into every admin asset and route. `web/` supplies only
appearance, branding and identity types; this app imports no workbench state.
Repository checks, standalone packaging and real-edge verification include both
frontends; see [verification](../docs/verification.md).
