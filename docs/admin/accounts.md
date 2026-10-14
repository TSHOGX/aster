# Accounts, groups and permissions

[Documentation](../README.md) · Administration

Sign in with an administrator account and open `/admin`. Use **Users**, **Groups**,
**Policies** and **Audit** to manage access. Host operators create the first
administrator during [installation](../operators/installation.md).

## Create an account or reset a password

1. In Users, create the account with its intended role and group memberships.
2. Review its effective permissions and resource allowances.
3. Deliver the one-time temporary password through your approved private channel.
4. Ask the user to sign in and choose a permanent password.

Temporary passwords are shown once, expire after 24 hours and permit only password
setup. The setup session lasts 15 minutes. A retried response cannot reveal the
same temporary password. If the original response is lost, first determine whether
the operation succeeded; issue a new reset if the user needs a replacement credential.

Passwords require 12–128 Unicode characters and at most 512 UTF-8 bytes. Password
replacement revokes previous sessions. Usernames remain reserved when accounts
are disabled; resource ownership uses a stable account identity, not the display name.

## Set permissions through groups

1. Create a policy and set the required actions to Unset, Allow or Deny.
2. Bind policies to a group and add its members.
3. Configure that group's resource policy and default templates when needed.
4. Open a user's effective-access view to check the resulting permissions and
   their group/policy sources. Do not assume one grant overrides every other constraint.

Project management, Agent execution, terminal use, scheduling, file actions and
sharing have separate permissions. File export also requires read permission.
Granting an environment does not automatically grant its shared directories.
Use [Resources](resources.md) to configure those grants and defaults together.

Preview group deletion before confirming it. Review affected users, lost grants
and resource changes. Deleting a populated group does not silently assign a
replacement membership. Refresh a stale preview and review it again.

## Suspend an account

Suspension disables login, revokes sessions and service access, pauses schedules,
holds unstarted prompts and suspends sharing. It also starts an operation to stop
active work and pause the account's Projects while retaining their data.

Follow the operation and its Project targets until their effects are observed.
**Accepted** or disabled account status does not mean every process has stopped.
A target can remain unknown while the engine is unavailable.

Re-enabling restores login and current policy access. It does not automatically
restart work. After suspension completes, the owner explicitly resumes Projects,
releases held prompts, resumes schedules and reactivates eligible shares as needed.
Account enablement cannot revive an expired or revoked public link.

## Review changes and sensitive access

Use Audit filters for actor, target, action and time, then follow operation links
for completion details. A save with an uncertain response should be retried using
the editor's retained request, not a new competing edit. If saving succeeded but
refresh failed, reload saved data before attempting another write.

Administrative conversation inspection is separate from routine Project metadata.
It requires current administrator authority and the explicit content-read grant,
and successful reads are audited with their purpose. It does not grant arbitrary
workspace, terminal or host-file access. See [Security](../reference/security.md).

## Group configuration drafts

Group configuration drafts are available through the authenticated API. An
administrator can assign `config.read` and `config.manage` separately with
`PUT /api/admin/groups/{group}/optimization/grants`. Supply `request_id`,
`account_id`, `action`, `effect` (`allow` or `deny`), and `expected_revision`
(zero for a new grant). Ordinary accounts also need current membership in that
group. An explicit deny takes precedence, including for administrators.

Use `GET /api/admin/groups/{group}/optimization/draft` to read the latest draft
and `PUT` at the same path to save it. Saving requires `request_id`,
`expected_revision` (the draft revision), and `configuration`. The configuration's
`group_id` must match the path, `revision` must be zero, and `auto_enabled` must
be false. Incomplete drafts are allowed. A save does not activate configuration
or change running work. On a revision conflict, read the latest draft before
reapplying edits; retry an unchanged request with its original request ID.

These requests use the normal login session and same-origin mutation checks.
Configuration grants do not confer source access or permission to publish content.

To check a saved draft, POST to
`/api/admin/groups/{group}/optimization/preview` with `expected_revision` (the
current desired configuration revision, initially zero),
`expected_draft_revision`, and `auto_enabled`. The response includes central
validation issues and the required participants. `central_valid` does not mean
Studio or Evaluation Space has accepted the configuration. Preview does not
reserve output paths or start work.

POST the same fields plus `request_id` to the `/activate` endpoint to create a
pending activation. This requires both configuration read and manage access.
A `202` response acknowledges the operation; it does not confirm activation.
Read `/api/admin/groups/{group}/optimization` to compare desired and active
revisions. When both participant clients are configured, the worker reserves Studio outputs,
reconciles the Evaluation Space projection, adopts Studio reservations, and marks
the revision active after both receipts are verified. A participant outage leaves
the operation pending for retry.

POST `request_id` and `expected_revision` to the `/disable` endpoint to disable
automatic admission immediately, including while activation is pending. This
requires manage access and retains the pending operation and active revision.
