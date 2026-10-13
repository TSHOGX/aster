# Documentation rules

1. Write public documentation in English for users, administrators and operators.
   Explain current tasks, prerequisites, outcomes, limits and recovery actions.
   Keep implementation details, development plans, release/acceptance records and
   installation-specific evidence in the ignored root `private/` directory; never
   link public guides to private material or include it in published artifacts.

2. Integrate behavior changes into the existing task guides under `users/`,
   `admin/`, `operators/` and `reference/`; do not append feature-release notes or
   create duplicate guides. Update `docs/README.md` when navigation changes,
   remove stale or contradictory guidance, and verify instructions against current
   behavior. Before finishing, run `python3 scripts/check-public-docs.py` from the
   repository root and check affected links, section anchors and command examples.
