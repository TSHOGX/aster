# Aster documentation

Choose a guide for the task you want to complete. **Users** work in projects;
**administrators** manage accounts and shared resources through `/admin`;
**operators** install and maintain the services on their hosts. One person may
hold more than one role. Feature availability depends on your assigned permissions.

## User guide

1. [Get started](users/getting-started.md): sign in and complete your first project task.
2. [Projects and conversations](users/projects.md): organize work, queue prompts, pause and delete.
3. [Files](users/files.md): upload, find, preview, edit and download.
4. [Terminals](users/terminals.md): run commands, reconnect and stop processes.
5. [Scheduled work](users/scheduling.md): create recurring tasks and bounded checks.
6. [Sharing and Inbox](users/sharing.md): share conversations, publish snapshots and read notices.
7. [Account settings](users/settings.md): appearance, password, resource usage and defaults.
8. [Troubleshooting](users/troubleshooting.md): recover from common problems without duplicating work.

## Administration

1. [Accounts, groups and permissions](admin/accounts.md).
2. [Resources, environments and networks](admin/resources.md).
3. [Inspect and control projects](admin/projects.md).
4. [Publish environment data updates](admin/environment-updates.md).
5. [Author and import files with Volume Studio](admin/volume-studio.md).

## Deployment and operations

1. [Install Aster](operators/installation.md): prerequisites, artifacts, bootstrap and startup.
2. [Configure the installation](operators/configuration.md): settings, service gateways and credentials.
3. [Maintain and monitor](operators/maintenance.md): diagnostics, notices and account recovery.
4. [Upgrade services](operators/upgrades.md): select the update mode, drain and activate.
5. [Back up and recover](operators/recovery.md): backups, unknown outcomes and deliberate reset.
6. [Manage runtime and content inventory](operators/runtime-inventory.md).
7. [Deploy and back up Volume Studio](../volumes-studio/deploy/README.md).

## Reference

- [Terms and lifecycle](reference/concepts.md).
- [File preview support and limits](reference/file-previews.md).
- [Security and privacy](reference/security.md).
- [Deployment compatibility](reference/compatibility.md).
- [Third-party notices](third-party-notices.md).

All commands use example paths and identifiers. Replace them with the reviewed
values for your installation. Keep real configuration, credentials, inventories,
development notes and operation records in the ignored `private/` directory or
an access-controlled location outside the checkout. They are not public guides.
