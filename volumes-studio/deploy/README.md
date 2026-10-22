# Deploy and back up Volume Studio

Volume Studio serves `/volumes-studio/` behind an independent HTTPS front door.
It uses central Aster accounts with scoped library grants and keeps its own authoring state,
blobs and application sessions. Aster imports source versions into separate local
storage. Ready Aster imports remain usable while Studio is offline.

This guide also ships in the independent Studio bundle. Commands below use example
paths: replace origins, paths, publisher identity and secret references with your
reviewed installation values. Store actual configuration and operation records in
`private/` or protected storage outside the repository.

## Prepare the bundle and services

Use a trusted bundle matching the host platform. To build from a committed checkout
into a new directory outside the checkout:

```sh
python3 volumes-studio/scripts/release.py build /srv/candidates/studio-new
python3 volumes-studio/scripts/release.py verify /srv/candidates/studio-new
```

The bundle provides `bin/studio`, `bin/front-door`, `bin/studio-backup`, the
standalone frontend, deployment guidance and `studio-release.json`. Verification
rejects changed, missing or extra files. Preserve the trusted source/artifact
identity; an inventory checksum alone does not authenticate an arbitrary bundle.
Building from a checkout does not include uncommitted work or private configuration.

Supervise frontend, Studio and front door independently. Keep the Studio state
directory owner-only and preserve its publisher ID across restarts and restores.

| Setting | Purpose / default |
| --- | --- |
| `-state-directory` | Required absolute private persistent directory |
| `-publisher-id` | Required stable publisher identity |
| `-public-origin` | Public HTTPS origin, also the central identity issuer |
| `-listen` | Private Studio gateway, `127.0.0.1:8082` |
| `-frontend` | Private frontend origin, `http://127.0.0.1:3002` |
| `-identity-endpoint` | Private central identity origin |
| `-identity-client` | Registered client ID, default `studio` |
| `-identity-secret-env` | Environment name for the central client credential |
| `-quota-bytes` | Blob/upload reservation budget, default 8 GiB |
| `-distribution-config` | Optional private JSON file of service-client registrations |
| `-distribution-listen` | Private distribution address, `127.0.0.1:8084` |

## Register central identity

Add an identity registration to Aster's private configuration. This example uses
`https://example.com:8443`; the origin must match all services exactly:

```json
{
  "identity": {
    "issuer": "https://example.com:8443",
    "listen": "127.0.0.1:8083",
    "clients": [{
      "id": "studio",
      "callback": "https://example.com:8443/volumes-studio/auth/callback",
      "secret_envs": ["STUDIO_IDENTITY_CLIENT_SECRET"]
    }]
  },
  "front_door": {
    "public_origin": "https://example.com:8443",
    "peer_ip": "127.0.0.1"
  }
}
```

This is a configuration fragment. Supply the referenced credential to the central
service and Studio through their protected environments. Secrets must be 32–1024
bytes. During rotation, central registration can accept two secret references;
update the client and then remove the previous credential. Include Aster configuration
changes in its normal service-upgrade procedure.

Callbacks must match exactly. The identity adapter is private and is not a general
OIDC provider. Keep its loopback listener private; cross-host access requires an
authenticated TLS tunnel. Studio sign-in requires an active, unrestricted central
account with a library read grant, or an administrator account. Administrators can
create libraries; read, author, review, merge and publish actions require separate
library grants. Configure access separately controls Volume policies. Assign these
in Aster Administration: open the account or group, choose **Studio permissions**,
enter the library ID, refresh, and review each allowance or denial. The built-in
shared-assets library ID is `aster-shared-assets`. Account inspection includes
inherited group denials; administrator status never overrides them. Studio cannot
grant those permissions locally.

Identity outages deny privileged operations rather than extending cached authority.
Retry sign-in after service recovery. A lost callback exchange needs a fresh login.
Ordinary logout does not cancel admitted publication work; account disable/demotion
can pause its next phase for reassignment.

## Start the frontend and Studio

From the verified bundle root, start the frontend as a supervised service:

```sh
HOSTNAME=127.0.0.1 PORT=3002 node frontend/server.js
```

Start Studio separately, with its credential available in the named environment:

```sh
bin/studio \
  -state-directory /var/lib/volume-studio \
  -publisher-id deployment-publisher \
  -public-origin https://example.com:8443 \
  -identity-endpoint http://127.0.0.1:8083 \
  -identity-client studio \
  -identity-secret-env STUDIO_IDENTITY_CLIENT_SECRET \
  -frontend http://127.0.0.1:3002
```

Studio's business listener requires loopback behind the front door. Its application
sessions survive a normal restart, but ongoing browser uploads stop when the browser
closes; users can reselect the same file to resume acknowledged data.

## Start the public front door

With both business services reachable privately:

```sh
bin/front-door \
  -listen 0.0.0.0:8443 -public-origin https://example.com:8443 \
  -application http://127.0.0.1:8080 -studio http://127.0.0.1:8082 \
  -cert /etc/volume-studio/public.crt -key /etc/volume-studio/public.key
```

Set Aster's trusted peer to the proxy's actual source address. Restrict business
listeners to the front door. Remote upstreams require verified HTTPS; loopback IPs
may use HTTP. Do not publish Studio distribution through this proxy.

The front door preserves streaming and file ranges, checks the public Host and
filters cookies for each application. Unexpected upstream cookies fail the response.
Studio receives only its own cookies, scoped to `/volumes-studio/`; it does not
receive the Aster login cookie. Stopping Studio does not stop the Aster route, but
the shared proxy and central identity remain deployment dependencies.

When Evolution Space is installed, add `-evolution-space` with its separate
browser gateway origin to the front-door command, for example
`-evolution-space http://127.0.0.1:18485`. Its public prefix is `/evolution-space/`
and its cookies are isolated from Studio and Aster. The unconfigured route returns
unavailable. Never point this option at the private worker listener.

Before enabling access, check central login, Studio library creation, save/upload,
publication, logout and the Aster route. Confirm assets and deep links load under
`/volumes-studio/`, including on the browsers used by your organization.

## Private distribution

To permit Aster imports, create a private JSON registration file:

```json
{
  "clients": [{
    "id": "aster",
    "secret_envs": ["STUDIO_DISTRIBUTION_SECRET"],
    "grant_revision": 1,
    "libraries": ["*"]
  }]
}
```

Prefer explicit library IDs over `*` for restricted consumers. Supply the secret
in Studio's environment and start it with the additional arguments:

```sh
-distribution-config /etc/volume-studio/distribution.json \
-distribution-listen 127.0.0.1:8084
```

These are arguments to the `bin/studio` command, not a separate command. Register
the same client/secret and exact publisher ID in Aster's `volume_imports` settings.
The consumer endpoint must be reachable from Aster's service UID; use a reviewed
TLS tunnel where loopback access is unavailable or hosts differ. Distribution
itself requires a private loopback listener. Never expose it through the public proxy.

Two secret references permit rotation. Remove a credential to stop its new requests;
increment `grant_revision` to invalidate all outstanding exports for that client.
Registration changes require a Studio restart. Service requests carrying browser
Origin or Cookie headers are refused.

Exports expire after 15 minutes and require service credentials for every read.
Archiving stops new discovery, exports and renewals; admitted readers can finish
within their bounded access. Retirement waits for readers/validation and removes
source references. It does not delete or revoke consumer copies already imported.

## Backup and restore

Stop Studio before taking its supported backup. The command refuses a running
writer. Choose a new empty destination outside the source state directory:

```sh
bin/studio-backup \
  -state-directory /var/lib/volume-studio \
  -publisher-id deployment-publisher \
  -output /srv/backups/volume-studio-snapshot
```

The snapshot contains authoring metadata, blobs and acknowledged uploads, with file
digests in `backup.json`. An interrupted backup retains `backup.incomplete` and
cannot be started as a restored instance. Preserve the complete private backup,
publisher identity and matching configuration; do not copy just the database.

Restore to an independent directory and verify the inventory against trusted backup
evidence. Start with the original publisher ID and the restored paths. Never run a
writable restore against the source blob directory. Application sessions are excluded:
users sign in again through central identity. Draft/version history and acknowledged
uploads are retained independently of sessions.

## Upgrade Studio

1. Prepare and verify the selected independent Studio bundle.
2. Stop Studio under supervision and take a complete verified private backup.
3. Activate the bundle's backend/frontend as a matched set. Replace the front door
   separately if required, planning its effect on both applications' browser access.
4. Start Studio with the same publisher ID and reviewed state/configuration.
5. Check central login, draft/version history, resumed upload, published content and
   the private distribution catalog before restoring normal traffic.

Studio creates or verifies the complete current v1 authoring and identity schemas.
It does not migrate existing databases. Unknown versions and mismatched structures
are refused without conversion. During development, stop Studio and rebuild its
state and bundle together when the format changes; retaining historical data is
not supported by this procedure.

Studio's independent release does not require draining Aster. Aster-side imports
and publication work still participate in Aster's own maintenance blockers. Ready
local imports remain available during a Studio update.
