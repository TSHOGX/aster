# Independent public front door

Build the proxy separately from Aster and Studio:

```sh
go -C volumes-studio/backend build -o /srv/volume-studio/bin/front-door ./cmd/front-door
/srv/volume-studio/bin/front-door \
  -listen 0.0.0.0:8443 -public-origin https://example.com:8443 \
  -application http://127.0.0.1:8080 -studio http://127.0.0.1:8082 \
  -cert /etc/volume-studio/public.crt -key /etc/volume-studio/public.key
```

Origins and certificate paths are operator configuration, never browser input.
Remote upstreams require verified HTTPS; loopback IPs may use HTTP. Both business
listeners must be private and accept traffic only from the front door. Studio's
distribution listener is separate and never exposed by this proxy.

When Aster sits behind this TLS terminator, add the following to its configuration:

```json
{"front_door":{"public_origin":"https://example.com:8443","peer_ip":"127.0.0.1"}}
```

Aster rejects other peers/hosts and reconstructs the original HTTPS authority from
configuration. Only that peer supplies the client IP. Direct Aster deployments
omit `front_door`. Keep the peer address aligned with the proxy's actual source
IP, and include this change in the normal Aster release configuration review.

The front door preserves query, ranges, conditional headers and event streaming,
checks the public Host, strips spoofed identity/forwarding headers, and filters
request cookies for each destination. Studio responses may set only its session
and login cookies with `Secure; HttpOnly; SameSite=Lax; Path=/volumes-studio/` and
no Domain. Unexpected cookies fail the entire upstream response. Aster receives
only `project_login`. Public `/distribution/` routes are refused.

The proxy has request-header/idle/upstream-header deadlines; streaming bodies
retain cancellation without a whole-response deadline. Each application must
bound its individual upload/read operations. Stopping either business process
does not take the other route down. The proxy and sole identity authority remain
explicit deployment dependencies.

Build and run the independent Studio gateway:

```sh
go -C volumes-studio/backend build -o /srv/volume-studio/bin/studio ./cmd/studio
/srv/volume-studio/bin/studio \
  -state-directory /var/lib/volume-studio -publisher-id deployment-publisher \
  -public-origin https://example.com:8443 \
  -identity-endpoint http://127.0.0.1:8083 \
  -identity-client studio -identity-secret-env STUDIO_IDENTITY_CLIENT_SECRET \
  -frontend http://127.0.0.1:3002
```

Register the same public issuer/callback and secret environment in Aster's
[identity adapter](../contracts/v1/identity.md), including its private loopback
listener. Studio's gateway listens on loopback behind the front door. It never
receives `project_login`, and never forwards its session cookie to the frontend.

The first implementation performs fresh introspection on every privileged request;
it does not use a local authorization cache. Identity outages immediately return
`identity_unavailable`; login and logout can be retried after recovery. Central
evidence itself is bounded to at most 60 seconds, and no response or local session
extends that interval. Admitted worker phases use the separate principal check.

`sessions.db` contains only login attempts and app sessions referencing central
authority. Local cookie tokens are stored as digests; back-channel grant references
are private credentials needed for introspection. Keep the directory owner-only.
SQLite uses WAL/FULL, foreign keys, an exclusive owner lock and transactional schema
steps. Unknown newer schemas fail without reset. A lost callback exchange requires
a fresh login; durable app sessions survive a normal Studio restart.
