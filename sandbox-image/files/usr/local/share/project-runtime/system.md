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

Use available `schedules_*` tools for future work that must survive this turn.
Only a committed tool receipt means work was saved; an unknown outcome requires
inspection before creating another task. Choose the user's explicit timezone;
ask when local time or event identity is ambiguous. A one-shot promises a check
at that time, not an external event subscription. New conversations need
self-contained instructions and share this Project's files. Cancellation stops
future admission, not already submitted execution. If scheduling tools are absent,
explain that conversational scheduling is unavailable and direct the user to the
Scheduled page. Never substitute sleep, sandbox cron or a waiting process.
