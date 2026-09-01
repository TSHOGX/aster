# R0 platform foundation

R0 establishes the control-plane contracts shared by Cron Agent, the admin
console, Skill Store, and semantic-layer evolution. It is deliberately useful
with all product features disabled: an ordinary session continues to use the
existing API, while administrator APIs are dark unless a credential is marked
`role: admin`.

## Design principles

* **One identity source for the request.** The hot-reloaded credentials file
  authenticates the user and supplies the role. SQLite stores the durable
  projection needed by sessions and audit history; it is not a second password
  database. Existing files without `role` parse as `user`.
* **Deny by default.** Missing credentials, open/lab mode, unknown roles, and
  malformed role values cannot enter `/api/admin/*`. Feature flags are seeded
  disabled and are changed only through an authenticated admin route.
* **Audit at the mutation boundary.** Every admin mutation records actor,
  action, target, before/after JSON, reason, request id, and UTC time. Audit
  failure never turns a successful state change into a false failure; operators
  can detect it through the returned error/log and retry the query.
* **Additive, restart-safe migrations.** New tables use `CREATE TABLE IF NOT
  EXISTS`; `users.role` is added with a constant default. No existing row is
  rewritten and old binaries continue to read the database.
* **Bounded reads and writes.** Audit queries cap page size at 200. Flag reads
  are small point/list queries and can later be cached without changing the
  API contract.

## Contracts shipped in R0

* `GET /api/feature-flags` — authenticated users receive the current rollout
  records; values never come from build-time frontend environment variables.
* `GET /api/admin/feature-flags` — admin-only complete flag records.
* `PATCH /api/admin/feature-flags/{key}` with `{enabled, description?, reason?}`
  — admin-only, durable update plus audit entry.
* `GET /api/admin/audit?limit=&offset=` — admin-only, newest-first bounded
  audit history.

The route subtree is wrapped in a dedicated `requireAdmin` middleware. This
keeps authorization structural: adding a normal `/api/*` handler cannot
accidentally expose an admin handler, and an admin handler cannot accidentally
fall through to the user router.

## Migration and rollback

Deploy the new binary against the existing control database; startup performs
the idempotent migration before opening the read pool. To roll back the binary,
stop the server and run the previous version: it ignores the additive tables
and the extra `users.role` column. Do not drop the new tables during rollback;
doing so destroys audit evidence and makes a subsequent forward migration less
safe. If a physical downgrade is required, take a database backup first and
drop only `feature_flags`, `admin_audit_log`, and `users.role` in a maintenance
window after confirming no newer binary needs them.

## Follow-on slices

R1 can consume the read-only runtime/session queries without changing identity.
R2/R4 can gate Cron Agent creation and worker startup on `cron_agent`. R3/R5/R7
can use `skill_store` for catalog and publishing. R6/R8 can use
`evolution_admin`; all write paths should reuse `AppendAudit` and immutable
snapshot semantics rather than introducing parallel logs.
