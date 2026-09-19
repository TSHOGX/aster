# Reviewed schema fixtures

Generated using the real `store.Open` and `memorystore.Open` implementations at:

- `669542d4ab3453509a6872e996cdf38bc7331edf`: fresh old databases.
- `237f8b3b82cb6bc03f141da3df1f98e55445670e`: fresh target and target opened over old databases.
- `0ccddb5`: retained environment and historical journal schema before adding
  independent environment capacity observations.

The memory schema is identical in all three cases, so it has one fixture.
SQLite virtual-table shadow DDL is created by FTS itself; indexes and triggers,
including FTS triggers, are preserved in these fixtures. Production schema
fingerprints include the shadows as well as all application DDL.

Developer regeneration: `python3 scripts/release/generate-schema-baselines.py`
from a full-history checkout. Review all fixture/catalog diffs; do not add an
unknown production fingerprint merely to bypass a migration refusal. A dirty
binary's schema needs an independently inspected backup and migration rehearsal.
