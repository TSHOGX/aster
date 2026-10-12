# Shared identity adapter contract

Studio and Aster use one central account authority. Studio stores only opaque
application sessions and `(issuer, subject, central_session, auth_epoch)`. It has
no password, password hash, account status or second user directory.

The registered client uses a 60-second single-use authorization code bound to the
exact callback, client ID, PKCE-S256 challenge, central session and epoch. The
back-channel `POST /api/identity/v1/token` accepts `code`, `redirect_uri` and
`code_verifier` with a confidential client credential and returns issuer, stable
subject, central session, epoch, entitlements and a bounded expiry. Studio rejects
expired/malformed assertions and unsafe return paths. Identity authority is checked
within a 60-second maximum cache window; outages never extend it.

Example Aster configuration (credentials live only in the referenced environment):

```json
{"identity":{"issuer":"https://example.com","listen":"127.0.0.1:8083","clients":[{
  "id":"studio",
  "callback":"https://example.com/volumes-studio/auth/callback",
  "secret_envs":["STUDIO_IDENTITY_CLIENT_SECRET"]
}]}}
```

Each secret is 32–1024 bytes. A second configured environment reference accepts
the previous credential during rotation; removing it revokes that credential.
The adapter is a narrow private protocol, not a complete OIDC provider.

`GET /api/identity/v1/authorize` takes `client_id`, `redirect_uri`, `state`,
`code_challenge` and `code_challenge_method=S256`. The client and callback must
match registration exactly. No implicit or wildcard callbacks are accepted.
Unrecognized parameters and duplicate parameters are refused. An unauthenticated
user returns through the existing central sign-in and password-setup interface.
Studio is never sent an Aster cookie.

The token response includes `issuer`, `subject`, `central_session`, `auth_epoch`,
`entitlements`, `expires_at` and `grant`. The grant is an opaque client-bound
reference, persisted centrally only as a digest. It cannot authenticate to Aster.
`POST /introspect` accepts that grant and returns renewed bounded evidence.
All POST identity endpoints use Basic client authentication and JSON, refuse Origin
and Cookie headers, and must use private HTTPS or a protected loopback transport.
The adapter mounts POST endpoints only on its explicit private loopback listener;
the public application exposes only `authorize`. The public front door strips
Authorization. Studio configures the private endpoint, such as
`http://127.0.0.1:8083`, separately from the public issuer. Cross-host deployment
needs an authenticated TLS tunnel to that private listener.

`POST /revoke` accepts the client grant and revokes the originating central
session and all its derived app grants. Ordinary Aster logout uses the same
lineage. Account disable, demotion, password change and all-device logout deny
old grants. A consuming app requires CSRF protection before initiating logout.
`POST /principal` accepts a stable subject for worker phase checks; administrator
status and account activation are authoritative. Ordinary browser logout does not
revoke a principal's admitted background work; disable/demotion blocks new phases.

The first release uses central active, unrestricted administrators for
`volumes_studio.author`. Studio cannot grant this entitlement locally. Codes,
grants and passwords must not enter logs or URL telemetry. Token/authority
responses are non-cacheable; callback pages use `Referrer-Policy: no-referrer`.
