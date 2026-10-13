# Install Aster

[Documentation](../README.md) · Deployment and operations

This guide is for host operators installing a new instance. An existing installation
needs the [upgrade runbook](upgrades.md). Aster does not migrate historical databases;
review [compatibility](../reference/compatibility.md) before choosing artifacts.

## Prepare the host and artifacts

Use a Linux host with the pinned Khora engine and its supported sandbox/storage
substrate. Runtime preparation requires namespaces, cgroup v2, btrfs with simple
quotas, runsc and the appropriate network tools. Provide sufficient persistent
storage for owned volumes, publications, transfer staging and backups.

Prepare these inputs before opening browser access:

- A trusted application candidate containing `bin/orchestrator`, `bin/edge-proxy`,
  workbench and admin standalone frontends, and `candidate.json`.
- A matching engine and immutable runtime template with its exported manifest
  and exact digest. Preserve the engine's persistent store identity.
- Reviewed Agent configuration and shared assets, TLS material, upstream credentials
  and private host configuration.
- Independent service supervision for engine/proxy, both frontends, application
  owner and any service gateway. The engine must use manual cold recovery.

The application state filesystem must support Linux `O_TMPFILE` for file captures.
Protect state and Unix sockets from unrelated host users. There is one active
application owner; active-active deployment is not supported.

If building your own host candidate, use a clean committed checkout, the Go version
in `orchestrator/go.mod`, Node from `.node-version`, and authorized access to the
pinned engine dependency. The destination must be new and outside the checkout:

```sh
python3 scripts/candidate.py build /srv/candidates/aster-new
python3 scripts/candidate.py verify /srv/candidates/aster-new
```

The build packages committed source, not local uncommitted edits. A successful
inventory verification checks integrity against its manifest, not artifact trust
or full runtime suitability. Use artifacts qualified for your host and workload;
keep qualification records private. Runtime assembly and registration are covered
in [runtime inventory](runtime-inventory.md).

To assemble the application, independent Studio bundle, pinned engine and a fresh
guest runtime together from one committed revision, use `scripts/rebuild.py` on
a privileged Linux builder with the runtime prerequisites above. See the
[installation tools](../../scripts/README.md) for arguments and current limitations.
The builder does not activate services. Use a new output directory and qualify
the complete build before deployment.

## Prepare configuration

Copy [application.example.json](../../config/application.example.json) to a private
configuration location such as `/etc/agent-workbench/application.json`. Review every
path: relative paths resolve against that file's directory, not the checkout.
Replace the engine store identity, manifest path/digest, service endpoints and
content paths. The example denies guest egress until you declare it.

Read [configuration](configuration.md) before enabling model gateways: deployments
using Khora's dedicated gateway UID need reachable **non-loopback** engine TLS,
frontend and model-upstream addresses. The example's loopback topology alone is
not a complete Agent deployment.

Keep the initial template/content/resource declarations for bootstrap. Confirm that
the referenced engine template is installed, that its manifest matches, and that
the content directories contain only reviewed material.

## Bootstrap the first administrator

With the application owner stopped, supply the password on standard input through
your approved secret mechanism:

```sh
orchestrator account bootstrap \
  --config /etc/agent-workbench/application.json \
  --username administrator --password-stdin
```

Use the verified candidate's executable for `orchestrator` in these commands.
Bootstrap creates the first administrator, standard group/policy, resource bounds
and initial environment registrations. It refuses to overwrite an existing account
database. Passwords are not command-line arguments; requirements are documented
under [account administration](../admin/accounts.md).

## Start the services

1. Start the engine and any authenticated engine proxy using the reviewed store.
2. Start the packaged workbench and admin frontend from the candidate directory:

   ```sh
   HOSTNAME=127.0.0.1 PORT=3000 node frontend/server.js
   HOSTNAME=127.0.0.1 PORT=3001 node admin-frontend/server.js
   ```

   Run these as separate supervised services. Replace bind addresses for the
   configured gateway topology and restrict direct frontend access.
3. Start the application owner:

   ```sh
   orchestrator serve --config /etc/agent-workbench/application.json
   ```
4. Publish the reviewed bootstrap content through the running owner:

   ```sh
   orchestrator publish --socket /var/lib/agent-workbench/control.sock \
     --config /etc/agent-workbench/application.json
   ```

5. Start a separately supervised gateway if service audiences are configured:

   ```sh
   orchestrator gateway --config /etc/agent-workbench/application.json
   ```

6. Enable the reviewed HTTPS browser edge/front door once private checks succeed.

Startup does not publish content or register templates implicitly. After publication
is confirmed, serving configuration may omit bootstrap template/content/size inputs;
keep engine identity and service/host settings. Retain bootstrap inputs privately
for audit and recovery planning.

## Check before admitting users

Check diagnostics and gateway status, sign in, inspect Resource Center, and create
a disposable Project. Confirm a native Agent reply, permitted network access,
file upload/download, terminal reconnect and paused file access. Check scheduling
if enabled, then remove only your disposable resources. Confirm the running
executable and frontend identities match the selected artifacts.

Create accounts and grant resources using the [administration guides](../admin/accounts.md).
For Studio, follow its separate [deployment guide](../../volumes-studio/deploy/README.md).
