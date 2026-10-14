# Resources, environments and networks

[Documentation](../README.md) · Administration

Use **Resource Center** to manage network templates, environment templates,
registered sandbox/volume inventory and shared directories. Policies grant access;
groups supply defaults; account settings can select among authorized choices.

## Make an environment available

1. Ensure the operator has registered an enabled, compatible sandbox release and
   any required content. Imported Studio versions must be locally ready.
2. Provision shared directories and wait for **ready** before using them in a template.
3. Select the immutable Restricted or Public network built-in and create or edit
   environment templates. Select exact source versions, destinations and access
   modes, then preview and save. Copy a built-in before customizing it.
4. Grant the templates through policies. Each policy grants at most one network
   and one environment template; grant shared-directory access separately.
5. Bind policies to a group and select defaults granted by that group's own policies.
6. Check the user's effective configuration and resources before creating a Project.

Distinct defaults from multiple groups require an administrator selection; a user
preference cannot resolve that conflict. Clearing an administrator selection returns
the account to group inheritance. Only enabled and authorized templates are eligible.

## Choose the right directory type

| Type | Use and update behavior |
| --- | --- |
| Project-owned directory | Independent writable or read-only data owned by the Project; initial content is copied once |
| Immutable publication | A fixed shared read-only source version; compatible data versions can be updated through an explicit deployment |
| Shared directory | A separately provisioned mutable directory available to authorized users; effective access also respects the environment's limit |

Workspace and runtime storage have required roles. The selected release fixes
required system-publication locations and protected paths. Optional data mounts
must not conflict with them. Template validity does not reserve capacity or raise
an account's quota.

Saving a template changes the defaults for **new Projects**. Existing Projects
retain their saved environment and owned data. To update compatible immutable data
for existing Projects, use [environment updates](environment-updates.md). This
cannot replace their runtime image, resize storage or reset owned copies.

## Configure network access

Direct rules allow IPv4 CIDR, port and protocol (`tcp` or `udp`) combinations.
They are not domain-name or HTTP filters. DNS resolvers authorize UDP port 53;
TCP fallback needs an explicit TCP/53 rule. DNS alone does not grant HTTPS access.

The Restricted built-in has no direct or DNS rules and is the standard-user
default. The Public built-in permits TCP ports 80 and 443 for `0.0.0.0/0` plus
deployment-provided DNS resolvers. This wildcard covers public IPv4 ranges; it excludes private,
loopback, link-local, shared, multicast and reserved ranges. It does not grant
IPv6 or other ports. More specific rules and engine restrictions still apply.
The operator must also provide functioning forwarding/NAT and upstream access.

Operator-defined model service routes and host mappings are separate from editable
network templates. Template or preference edits do not silently replace a running
Project's network. An explicit existing-Project change takes effect through pause
and resume. Use [Project controls](projects.md) when immediate interruption is intended.

## Set resource allowances

Group **Resource policy** sets complete default/ceiling pairs for each offered
dimension. User **Resources and usage** can inherit or carry a requested override.
An omitted group dimension makes no offer; zero is an explicit zero allowance.
Effective values combine group grants, platform bounds and user requests. Review
source groups and capped requests in the effective view.

Preview changes before confirming. Review usage overages, environment fit and
saved Project capacities. Lowering limits preserves data and allocations but can
block new provisioning or resume; it does not shrink storage. Cleanup remains
available. The operator controls platform caps through the protected command line.

## Interpret inventory and save failures

Inventory shows version identity and availability. **Unknown** or **unchecked**
is not confirmation that an artifact is usable; refresh or ask the operator to
check the engine. A ready local import can remain usable while its Studio source
is offline. Registration and availability do not reserve resources.

Save only after reviewing the impact preview. If dependencies changed, refresh
that preview rather than confirming stale information. The editor retains changes
on conflicts and retains the original request after an uncertain response. A
successful save followed by a failed refresh needs a reload, not a duplicate save.
