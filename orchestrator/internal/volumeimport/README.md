# Consumer import boundary

`volumeimport` is the Aster side of the Studio distribution contract. Admission
binds one configured source, logical version, descriptor digest and consumer
profile. The persisted job advances through `acquiring`,
`source_acquired` or `quarantined`; checkpoints are monotonic and bounded by the
manifest's exact decimal byte count. Admission persists the exact canonical
descriptor before job metadata. A lifetime file lock excludes a second owner.

`Append` writes bounded sequential chunks and fsyncs the blob and directory before
persisting an acknowledgement. Identical retries compare accepted bytes. Complete
files, including zero-byte files, receive a streamed digest check before the
verified bit is persisted. Blob digests shared by multiple paths transfer once.
On reopen, incomplete files truncate to accepted offsets and complete files are
reverified. Corrupt or missing acknowledged content fails closed.

The source descriptor and all file bytes are verified before `source_acquired`.
After that state Aster can continue engine preparation without contacting Studio.
Unknown or contradictory transfer outcomes can be quarantined while retaining the
identity and byte evidence. The caller owns quota admission and must retain its
reservation and cache until all readers and runtime work settle. A write failure
fences the current handle until reopen; quarantine is persisted explicitly.
This package does not allocate engine objects;
that phase must preserve the existing staging, keyed snapshot and readback
verification lineage.

```sh
go -C orchestrator test ./internal/volumeimport ./internal/volumeimport/manifest
```
