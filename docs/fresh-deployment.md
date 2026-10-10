# Fresh development deployment

The 2026-10-10 development installation runs the conversational scheduling release
with schema 4, runtime manifest 4, native protocol 4 and schedule protocol 1.
The [sanitized deployment record](../dev/measurements/current-deployment.json)
identifies the exact committed candidate, installed artifacts and completed checks.
The subsequent scheduling UI refinement used a quiescent update, preserving the
existing Project and conversations. It adds minimal text actions, wrapping status
filters and consistent conversation cards; 44 focused browser cases and the
packaged real-runtime browser suite passed.

The authorized reset replaced the application database, captures, engine store,
owned volumes and runtime templates. Login/provider credentials, TLS and reviewed
configuration inputs were preserved. All supervised application components now use
one candidate; the configuration has one runtime binding and enables native
scheduling for the declared Agent gateway audience.

Qualification exercised the packaged frontend and owner with the fresh measured
image: real Pi scheduling tools and guest routing, one-shot delivery, bounded-check
finish, network policy, paused Files, mixed workload, browsers and fault recovery.
A separate installed-service smoke checked browser authentication, actual model
selection of the native scheduling tool, its persisted card and automatic result.
Disposable smoke resources and the temporary login were removed afterward.

This is a measured installation record, not automatic authorization to discard
another installation's data. Follow [delivery](delivery.md), [bootstrap](bootstrap.md)
and [operations](operations.md) for subsequent work. No migration or compatibility
adapter is provided. Physical power-loss testing and the optional combined-load
and external-provider handoff suites are outside this qualification.
