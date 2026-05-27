# print-cam

Secure browser-hosted printer camera prototype.

## Local development

```sh
GOCACHE=/tmp/print-cam-gocache go run .
```

Start the frontend separately:

```sh
cd frontend
npm install
npm run dev
```

Open `http://localhost:5173/login`.

The Vite app calls `VITE_API_BASE` when it is set. For local development it
defaults to the current browser origin, so set it when running the frontend on a
different port:

```sh
VITE_API_BASE=http://localhost:8080 npm run dev
```

## Production build

Build the frontend first, then start the Go server from the repository root:

```sh
cd frontend
npm ci
npm run build
cd ..
GOCACHE=/tmp/print-cam-gocache go run .
```

By default the server serves the built Vue app from `frontend/dist`, including
SPA fallback routes such as `/login` and `/cameras`. Override that path with
`FRONTEND_DIST_DIR` if the frontend assets are copied somewhere else.

## Container image

The included Dockerfile builds the Vue frontend, compiles the Go server, and
copies both into a small runtime image:

```sh
docker build -t print-cam .
docker run --rm -p 8080:8080 \
  -e DATABASE_URL='postgres://...' \
  -e PUBLIC_ORIGIN='https://print-cam.example.com' \
  -e SESSION_SECRET='replace-with-at-least-32-random-bytes' \
  -e BOOTSTRAP_PASSWORD='replace-before-first-run' \
  print-cam
```

The container listens on `:8080` and serves frontend assets from
`/app/frontend/dist`.

## Verification

Run the same checks locally before shipping changes:

```sh
make verify
```

That command runs the Go tests, builds the Vue frontend, installs the Chromium
browser used by Playwright, runs browser smoke tests against the production
preview build, and builds the production container image. The GitHub Actions
workflow in `.github/workflows` runs the same gates on pushes and pull requests.

Postgres integration tests run when `TEST_DATABASE_URL` is set:

```sh
docker run --rm --name print-cam-postgres \
  -e POSTGRES_USER=print_cam \
  -e POSTGRES_PASSWORD=print_cam \
  -e POSTGRES_DB=print_cam_test \
  -p 5432:5432 postgres:17-alpine
```

In another shell:

```sh
TEST_DATABASE_URL='postgres://print_cam:print_cam@localhost:5432/print_cam_test?sslmode=disable' \
  make test-integration
```

CI starts the same Postgres service automatically and runs the integration tests
as part of `go test ./...`.

The app reads `.env` automatically. Set `DATABASE_URL` to your Neon Postgres
connection string before starting the server. A pooled Neon connection string is
preferred for deployed app usage. Use `.env.example` as a template; never commit
the real `.env`.

Default bootstrap account:

- Email: `admin@example.com`
- Password: `change-me-now`
- TOTP secret: `JBSWY3DPEHPK3PXP`

Override these with `BOOTSTRAP_EMAIL`, `BOOTSTRAP_PASSWORD`, and
`BOOTSTRAP_TOTP_SECRET`. HTTPS deployments refuse to start if the default
bootstrap email, password, TOTP secret, or an unset/short `SESSION_SECRET` is
still configured.

## Configuration

- `ADDR`: listen address, default `:8080`
- `DATABASE_URL`: Neon Postgres connection string, required
- `FRONTEND_DIST_DIR`: built frontend directory served by the Go server, default
  `frontend/dist`
- `PUBLIC_ORIGIN`: expected browser origin for WebSocket checks, default
  `http://localhost:8080`
- `FRONTEND_ORIGINS`: comma-separated API/WebSocket origins for separate
  frontend development servers, default `http://localhost:5173`
- `SESSION_SECRET`: HMAC key for signed login cookies
- `TURN_URLS`: comma-separated STUN/TURN URLs, default
  `stun:stun.l.google.com:19302`
- `TURN_SHARED_SECRET`: coturn REST API shared secret for short-lived TURN
  credentials

## Deployment checklist

- Set `PUBLIC_ORIGIN` to the exact HTTPS origin users open in the browser.
- Set `SESSION_SECRET` to at least 32 random bytes.
- Change `BOOTSTRAP_EMAIL`, `BOOTSTRAP_PASSWORD`, and `BOOTSTRAP_TOTP_SECRET`
  before first startup.
- Use a pooled production Postgres `DATABASE_URL` with TLS enabled.
- Configure a real TURN server through `TURN_URLS` and `TURN_SHARED_SECRET`;
  STUN-only defaults are for development and easy networks.
- Keep `.env` out of source control and rotate bootstrap credentials after the
  first administrative login flow is replaced with user management.
- Run `make verify` before publishing an image.

## Current Scope

The backend now has password plus mandatory TOTP login, HTTP-only signed
sessions, CSRF checks for mutations, owned camera create/rename/delete flows,
WebSocket signaling with browser-side reconnects, STUN/TURN ICE configuration,
basic rate limits, security headers, Neon-backed users/cameras/sessions/audit
events, and in-memory signaling state. The frontend is a separate Vite Vue app
in `frontend/` using Tailwind CSS.

The media stream is never handled by the Go server. The host browser captures the
camera and sends encrypted WebRTC media directly to the viewer, using TURN relay
when the browser ICE negotiation needs it.

On startup the server runs embedded SQL migrations from `migrations/`,
idempotently seeds the bootstrap user plus one default camera, cleans up expired
sessions, and starts health endpoints at `/healthz` and `/readyz`. The media
stream is still never stored in Postgres.

WebSocket signaling uses `nhooyr.io/websocket`; video remains browser-to-browser
WebRTC.
