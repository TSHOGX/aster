# Security and privacy

[Documentation](../README.md) · Reference

## Project boundaries

Agents and terminals within one Project share a Linux execution trust domain,
files, processes and readable runtime state. Separate conversations are not a
security boundary against hostile sibling work. Use separate Projects when you
need separate sandbox and resource authorization.

File permissions separately control list, read, export, upload, edit and management.
Preview content is intentionally copyable; these permissions are not DRM. A shared
directory can expose data to multiple authorized users. Hiding it from Files does
not remove underlying Agent access.

## Accounts and sessions

Normal sessions last seven days. Password changes, resets and sign-out-all revoke
earlier sessions. Temporary passwords expire after 24 hours and only establish a
15-minute password-setup session. Protect account credentials and sign out on shared
browsers. Report unexpected access to your administrator.

Permissions are rechecked for live and background work. Losing a grant can prevent
new dispatch and hold unstarted prompts; restoring it does not automatically release
those prompts or resume paused schedules. Suspension also revokes sessions, blocks
sharing and starts an operation to stop Project execution. Its completion is tracked
separately from disabled account status.

## Sharing and administrator access

Conversation-reader grants do not grant Files or terminal access. Public links are
bearer links to frozen selected content: anyone holding one may read it until it
expires or is revoked. Review content before publication. Token rotation invalidates
the previous token. Suspension blocks shares; re-enabling login alone does not
reactivate them.

Routine administrator Project metadata omits private conversation bodies and file
content. Administrative conversation inspection needs a separate explicit permission
and is audited. This is not unrestricted live workspace or host-file access.

## Operator responsibilities

Use TLS for externally reachable browser and engine endpoints. Keep private frontend
origins and Studio distribution listeners inaccessible to untrusted direct clients.
Configure the front door's exact origin and trusted peer; replace spoofed forwarding
headers rather than accepting arbitrary client identity.

Keep private application/control sockets and state directories accessible only to
the service operator. Upstream model secrets belong in the host gateway environment,
not guest files or public frontend build variables. Model gateway grants provide
attribution and protect host secrets; they do not isolate sibling Project processes.

HTML/Office previews restrict active content and external network access. Still
review untrusted files before opening them locally in an unrestricted application.
If portal embedding is unnecessary, restrict `frame-ancestors` at the outer browser
edge; the default application policy permits portal framing.

## Keep private material out of publication

Use the ignored `private/` directory or protected storage outside the repository
for real credentials, deployment inventories, databases, native histories, operational
logs, internal planning, implementation records and acceptance evidence. Never
force-add `private/` or include it in an image, release or CI artifact.

Public documentation contains reusable user/operator guidance and generic examples.
Runtime instruction assets and legal notices are shipped product material. Review
shared assets before deliberately publishing them: hashing a tree verifies identity,
not whether it contains private data. Source control and release archives are not
backups of the installation's database or workspace files.
