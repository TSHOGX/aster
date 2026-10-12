# Consumer import boundary

`volumeimport` is the Aster side of the Studio distribution contract. Admission
binds one configured source, logical version, descriptor digest and consumer
profile. The persisted job advances through `admitted`, `acquiring`,
`source_acquired` or `quarantined`; checkpoints are monotonic and bounded by the
manifest's exact decimal byte count.

The source descriptor and all file bytes are verified before `source_acquired`.
After that state Aster can continue engine preparation without contacting Studio.
Unknown or contradictory transfer outcomes can be quarantined while retaining the
identity and reservation evidence. This package does not allocate engine objects;
that phase must preserve the existing staging, keyed snapshot and readback
verification lineage.

```sh
go -C orchestrator test ./internal/volumeimport ./internal/volumeimport/manifest
```
