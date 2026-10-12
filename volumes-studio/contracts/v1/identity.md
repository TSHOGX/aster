# Shared identity adapter contract

Studio and Aster use one central account authority. Studio stores only opaque
application sessions and `(issuer, subject, central_session, auth_epoch)`. It has
no password, password hash, account status or second user directory.

The registered client uses a short lived single use authorization code bound to the
exact callback, client ID, PKCE-S256 challenge, central session and epoch. The
back-channel `POST /api/identity/v1/token` accepts `code`, `redirect_uri` and
`code_verifier` with a confidential client credential and returns issuer, stable
subject, central session, epoch, entitlements and a bounded expiry. Studio rejects
expired/malformed assertions and unsafe return paths. Identity authority is checked
within a documented 60 second maximum cache window; outages never extend it.
