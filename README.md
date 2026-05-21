# print-cam

Secure browser-hosted printer camera prototype.

## Run

```sh
GOCACHE=/tmp/print-cam-gocache go run .
```

Open `http://localhost:8080/login`.

Default bootstrap account:

- Email: `admin@example.com`
- Password: `change-me-now`
- TOTP secret: `JBSWY3DPEHPK3PXP`

Override these with `BOOTSTRAP_EMAIL`, `BOOTSTRAP_PASSWORD`, and
`BOOTSTRAP_TOTP_SECRET`. Set a production `SESSION_SECRET` before exposing the
app.

## Configuration

- `ADDR`: listen address, default `:8080`
- `PUBLIC_ORIGIN`: expected browser origin for WebSocket checks, default
  `http://localhost:8080`
- `SESSION_SECRET`: HMAC key for signed login cookies
- `TURN_URLS`: comma-separated STUN/TURN URLs, default
  `stun:stun.l.google.com:19302`
- `TURN_SHARED_SECRET`: coturn REST API shared secret for short-lived TURN
  credentials

## Current Scope

The app now has password plus mandatory TOTP login, HTTP-only signed sessions,
CSRF checks for mutations, owned cameras, host/viewer pages, WebSocket signaling,
STUN/TURN ICE configuration, basic rate limits, security headers, and in-memory
audit/session/signaling state.

The media stream is never handled by the Go server. The host browser captures the
camera and sends encrypted WebRTC media directly to the viewer, using TURN relay
when the browser ICE negotiation needs it.

Persistent Postgres storage is still the next deployment step; this prototype
keeps users, cameras, sessions, and audit events in memory.
