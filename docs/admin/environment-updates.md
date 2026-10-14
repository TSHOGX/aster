# Roll out environment content updates

[Documentation](../README.md) · Administration

Environment **Configuration** defines new-Project defaults. **Updates** explicitly
publishes compatible immutable content versions to existing Projects belonging to
that environment. Publishing in Studio, importing a version or saving a template
alone does not update running Projects.

## What can be updated

An update replaces compatible read-only immutable **data** publications. Mount
keys, destinations, roles and access must remain the same. A Studio source must
retain its publisher, library and configured source; an operator publication must
retain its artifact identity.

Shared-assets updates use a separate **Shared assets** update type. Select the
explicit Project set (up to 100), then review each current/target version and
blocker. Both versions must be validated shared-assets publications. An unsettled
prior operation must finish reconciliation before another rollout is admitted.

Updates cannot add/remove mounts, modify owned data, replace agent configuration
or runtime images, resize capacity, or change network rules.
An incompatible Project retains its previous data configuration; no partial
subset is silently applied. Owned workspace/runtime files remain intact.

## Preview and publish

1. Import or publish the desired source version and wait for local readiness.
2. Open **Resource Center → Environment templates** and select the environment.
3. In Configuration, select exact content versions and save the default for new
   Projects. Existing Projects retain their selection.
4. Open Updates and choose **Ordinary data** or **Shared assets**. For shared
   assets, select eligible Projects. Add a release note and preview the rollout.
5. Review compatible, already-current, fixed, incompatible and unauthorized
   counts. Page through Project differences before confirming.
6. Publish, then monitor the Projects and release-history views.

A changed target, grant or relevant configuration invalidates the preview; review
again. A deployment enrolls its existing Project cohort. Projects created later
are not added to the older deployment; they use their creation configuration.

## When updates apply

Eligible Projects in the confirmed cohort follow the rollout, including those not
yet started. Unselected Projects retain their current shared-assets version.
Application waits for the next natural start/resume. Long-running Projects can
remain waiting indefinitely without indicating failure. Use a separately reviewed
[Project restart](projects.md) only if interruption is intended.

Unstarted Projects select updated shared data before first compute startup.
Storage-only Projects retain any existing owned volumes. Locally ready content
is used without contacting Studio at activation time.

The Projects view shows current/target versions and follow policy. **Fix** keeps a
Project on its current selection; **Follow** resumes following. Review the target
again when switching back. An already admitted operation keeps its captured target
until its result is known, even if a newer release is published meanwhile.

## Stop, retry and roll back

**Stop** prevents new update effects; it does not undo applied changes. **Continue**
re-enables the current stopped deployment. A newer data publication supersedes the older data release. Separate shared-assets
cohorts remain active for their selected Projects; selecting a Project in a later
rollout replaces only that Project’s settled intent. Unknown effects can still need reconciliation after stopping.

A known safe failure can leave previous data usable. Automatic safe retries are
bounded to one attempt per activation and three per target. Use explicit Retry
after correcting the cause. An unknown binding can block execution until the
operator establishes what actually happened; retrying is not proof of failure.

**Rollback** publishes a new numbered update using retained content versions.
Shared-assets rollback also requires an explicit Project selection. It does not change the Configuration defaults for new Projects. Version
labels are names, not semantic-version ordering. If no earlier deployment exists,
select an earlier imported version in Configuration, save, then publish it explicitly.

Retire an old release only when it is not the latest and has no unsettled target
or execution dependencies. Retirement removes its rollback protection. Project
and pending-operation references can still prevent physical artifact cleanup.

## What users see

Daily work does not show update prompts or upgrade notifications. Users can inspect
read-only versions/status under **Settings → Advanced / Developer → Existing
projects → Environment**. A safe update failure does not turn ordinary Project
status into an error; a blocking unknown state can show a general availability
message. Administrative details remain in update execution history.

See [runtime inventory](../operators/runtime-inventory.md) for source retention
and [recovery](../operators/recovery.md) for unresolved outcomes.
