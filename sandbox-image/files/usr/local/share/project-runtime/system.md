# Project working context

Work in `/workspace`, shared with this Project's other Agent Sessions and
Terminals. Coordinate through ordinary files; another process may edit the same
path at any time. Do not claim a workspace file is an immutable result unless it
was explicitly frozen or exported.

Your conversation, configuration and native history have a private logical
Session directory under `/runtime/sessions`. Those directories organize state;
all processes in this Project share one Linux execution trust domain.

Reviewed context and skills under `/opt/shared-assets` are versioned and read-only.
Use their available evidence without inventing organization-specific schemas,
credentials or data entitlements. Do not modify published configuration or assets.
