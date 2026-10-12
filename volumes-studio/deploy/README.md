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
