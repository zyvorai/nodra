# Demo and customer walkthrough

Nodra ships a full customer-demo path: control plane console, A–Z fleet simulation, and verification scripts.

## One-command Kubernetes demo

```bash
./scripts/demo-k8s.sh
# or: make demo-k8s PORT=18080
```

This builds the image, loads it into kind (unless `--no-kind`), installs Helm with `values-demo.yaml` (control plane + edge agent + `nodra-sim`), and port-forwards the console.

| Item | Value |
|---|---|
| Sign in | `admin` / `nodra-demo-admin` |
| Console | http://127.0.0.1:18080 (or `--port`) |
| Teardown | `./scripts/demo-k8s.sh --uninstall` |

Existing cluster:

```bash
./scripts/demo-k8s.sh --no-kind
```

## Compose

```bash
docker compose up --build
```

Default UI port is `NODRA_PORT` (see compose file). Credentials match the demo admin token unless overridden.

## Console walkthrough

1. Open the control plane URL. The login gate uses Kryton-style chapters: **Product** → **This edge** → **Sign in**.
2. Sign in with `NODRA_ADMIN_USER` / `NODRA_ADMIN_PASSWORD` (password defaults to `NODRA_ADMIN_TOKEN`).
3. Walk the tabs:

| Tab | What you should see |
|---|---|
| Overview | Site/online counts, live activity preview |
| Sites | Enrolled edge sites; **Revoke** (admin) |
| Devices | Devices + twins; **Set desired** on twins (admin) |
| Streams | Routes; create form / seed / **Delete** (admin) |
| Apps | Deployments + alerts; create / start-stop / resolve (admin) |
| Dead letters | Replay + **Delete** (admin) |
| Logs | Mac Terminal–style activity stream |

Viewer login (`NODRA_VIEWER_*`) can browse but not mutate.


4. **Refresh fleet** reloads overview data. **Sign out** clears the session token and returns to the login gate.

## Fleet simulator (`nodra-sim`)

Seeds up to 26 lettered sites (A–Z), devices, twins, routes, and continuously posts heartbeats, telemetry, and detailed activity lines.

```bash
export NODRA_ADMIN_TOKEN=...
export NODRA_ENROLLMENT_TOKEN=...

./bin/nodra-sim \
  --server http://127.0.0.1:8080 \
  --once \
  --sites 26 \
  --state /tmp/nodra-sim-state.json
```

| Flag / env | Meaning |
|---|---|
| `--server` / `NODRA_SERVER` | Control plane base URL |
| `--admin-token` / `NODRA_ADMIN_TOKEN` | Admin bearer |
| `--enrollment-token` / `NODRA_ENROLLMENT_TOKEN` | Bootstrap enroll |
| `--sites` | 1–26 lettered sites |
| `--once` | Seed then exit (no loop) |
| `--interval` | Continuous loop period (default 8s) |
| `--state` / `NODRA_SIM_STATE` | Resume state file |

Activity lines appear under **Logs** and the overview live preview via `GET /api/v1/activity`.

## Lab / remote

```bash
./scripts/deploy-remote.sh 212.8.248.187 sus --port 20059
./scripts/smoke-remote.sh --port 20059
./scripts/demo-client.sh --port 20059
make test-all PORT=20059 HOST=212.8.248.187
```

Default lab smoke credentials: `admin` / `nodra-lab-admin` (token `nodra-lab-admin` unless overridden).

## Script map

| Script | Purpose |
|---|---|
| `scripts/demo-k8s.sh` | kind/Helm customer demo |
| `scripts/demo-client.sh` | API login + console client walkthrough |
| `scripts/smoke.sh` | Local control plane + agent smoke |
| `scripts/smoke-remote.sh` | Health, login, lists, activity against a live URL |
| `scripts/test-all.sh` | Build + local smoke + optional remote deploy/smoke + short sim |
| `scripts/deploy-remote.sh` | Build/push/restart remote systemd unit; writes `.deploy-last` |
