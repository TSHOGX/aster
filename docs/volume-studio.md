# Volume Studio and local runtime imports

Volume Studio is an independent application at `/volumes-studio/`. It owns libraries,
drafts, immutable source versions and authenticated distribution. Aster owns
acquisition, Khora materialization, registry availability, saved Environment/Project
references and runtime cleanup. The apps share one central account authority and
exchange versioned HTTP contracts; they share no writable database or blob paths.

## Author and publish

Open Studio using an active central administrator account. Create a library, add
explicit directories, edit UTF-8 text up to 1 MiB, or upload binary files/folders.
Ordinary files, empty files and empty directories retain exact paths and bytes.
Symlinks, special/executable promises, traversal, control characters, whitespace-edged
directory components and invalid parents are refused. Unicode is not normalized.

Changes use expected draft revisions. A stale save keeps your unsaved text and
requires reload/review. Resumable uploads acknowledge fsynced chunks and record
immutable chunk receipts. Select the same file after refresh to continue. Changing
a saved upload target requires explicit confirmation of its current identity;
refreshing the revision alone does not authorize replacement. Folder uploads are
individual operations with visible per-file outcomes. Unfinished server uploads,
including another tab's sessions, block publication until completed or canceled.

Publication review pins the revision being reviewed. Freeze copies immutable entry
rows, retains blob references and records a durable request receipt. A worker checks
central principal authority, verifies frozen source bytes and publishes the exact
canonical descriptor. Later draft edits cannot change that version. The worker
continues after browser logout; account disable/demotion pauses unfinished work for
explicit reassignment by another administrator. Transient identity failures retry
without extending authority. Closing a tab does not cancel durable work.

Text is inert editor data. Raster previews support PNG/JPEG/GIF/WebP up to 16 MiB.
PDF, SVG, Office and other binary formats use authenticated download fallback.
All content responses are attachments with `nosniff` and sandbox CSP; uploaded
HTML/JavaScript never becomes an active same-origin document. Rich document
renderers are outside this release's qualified preview set.

## Select and use in Aster

Resource Center → Volumes lists releases from configured trusted sources and local
imports. Import admission binds source, publisher, version, descriptor digest,
content profile, engine store, actor and reservations. A duplicate request replays
its receipt; different callers selecting the same qualified identity converge on
one import. Different source versions with identical content reuse compatible
materialization, while retaining separate provenance and registry references.

Acquisition checks canonical descriptors and whole-file hashes, fsyncs local byte
checkpoints and persists export receipts. Transfers use bounded authenticated
ranges. Once all bytes and metadata are `source_acquired`, Studio is no longer
needed. Aster streams the local source into staging, verifies it, uses a keyed
snapshot, verifies the derivative and atomically registers the ready local artifact.
Only ready registry references appear in the existing Environment editor.

Use the imported reference as shared read-only `data`, an initialized owned
`workspace`, or independent owned `data`. Paths, capacity and permissions remain
Aster Environment/resource policy. Publisher hints cannot authorize runtime or
protected system roles. Existing allocated and unallocated Projects keep their
saved Environment body. Editing a project-owned copy never reseeds it or compares
user changes against the original source during ordinary restart.

The `authored-v1` profile supports 256 MiB/file, 1 GiB/tree and 10,000 entries.
Trusted system/operator sources keep the smaller `reviewed-v1` limits. The profile
is retained in saved volume-source plans; readback hashes use bounded buffers.
Host filesystem source paths have been removed from immutable entry metadata in
favor of a source-reader interface.

## Failure and lifecycle

A source outage leaves locally ready artifacts usable. Interrupted transfers resume
from durable offsets; unacknowledged tails are discarded. Changed descriptors,
wrong publishers, corrupted bytes and unauthorized service grants never register
ready content. Catalog failure is shown separately from local import availability.

Create/snapshot keys include publication generation. Before an unkeyed population
write, Aster records an in-flight phase. An unknown write or lost settlement cannot
be retried or snapshotted on that staging object. Explicit retry retains its
quarantined attempt and capacity, rechecks budget and allocates a new generation
with new keys. Generation checks fence old local commits. At most three replacement
generations are allowed before operator investigation is required.

Studio archive stops new discovery/exports and renewals. Existing exports expire
within 15 minutes; admitted bounded readers hold durable pins until settled.
Retirement waits for export/read/validation blockers and drops only Studio source
references. Drafts and other versions can retain identical blobs. Source retirement
never deletes consumer copies or recalls already imported bytes.

Aster retirement checks registry aliases, saved Environment/Project bodies and
pending runtime effects. Physical cleanup observes child-before-parent deletion;
engine descendant or writer conflicts remain visible, charged blockers. Transfer
reservations are released only after local files are durably removed. Quarantined
attempts remain accounted until observed cleanup. Never equate declared capacity
with CoW filesystem usage.

## Operations and verification

See [source/proxy deployment, independent release and backup](../volumes-studio/deploy/README.md),
[central identity contract](../volumes-studio/contracts/v1/identity.md),
[portable manifest](../volumes-studio/contracts/v1/README.md), and
[trusted source configuration](configuration.md#trusted-studio-sources).
Studio author history uses ordered transactional migrations and its own backup;
Aster retains its exact-current-schema policy and refuses historical databases.

Local gates are `scripts/check.sh studio`, `studio-frontend`, `studio-browser`,
`backend`, and the existing admin/workbench suites. The browser gate runs a real
central identity adapter, independent Studio backend/frontend and TLS front door
with disposable state. The privileged `TestStudioImportOnRealKhora` gate launches
the real Studio binary in the existing disposable Khora substrate; set
`STUDIO_TEST_BINARY` and optionally `STUDIO_TEST_MAX_SIZE=1`. It verifies source
shutdown after acquisition, streamed materialization/readback, independent copies,
registration, descendant refusal and cleanup. Private evidence never enters release
artifacts. The [delivery tracker](volume-studio-delivery.md) records qualification.
