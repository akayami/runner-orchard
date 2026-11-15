# Console, collector and MCP

The first console is one Go executable serving a browser UI, read-only JSON API,
seven MCP tools, telemetry ingestion and Prometheus metrics. No database, Node
runtime, persistent volume or frontend build step is needed. One replica is
intentional: data is in memory and collectors must reach the same replica.

## Build and run

Go 1.27 or later:

```bash
GOMAXPROCS=2 go build -p 2 -trimpath -o dist/orchard ./cmd/orchard
cp config/server.example.json config/local.server.json
dist/orchard serve -config config/local.server.json
```

Open http://127.0.0.1:8787. Empty GitHub scope lists are valid; host telemetry can
work on its own. To see runners, edit scopes to `orgs/your-organization` and/or
`repos/your-organization/your-private-app`. Add active-job repositories separately
as `your-organization/your-private-app`. A scope is part of runner identity.

Set GITHUB_TOKEN in the server's environment or provide token_file in its config.
Use a credential permitted to read runner inventory and Actions data in the selected
scopes. Fine-grained permissions depend on whether the scope is an organization or
repository; test both endpoints. Do not grant workflow execution or management
permissions merely for this console. Private repository Actions read access is
needed for job details. The UI and collectors never receive the token.

This version reads the token at startup; restart after rotation. A manually
supplied GitHub App installation token works until expiry but is not renewed
automatically. Automatic App credential management is a later addition.

For Docker: `docker compose up --build` binds only to localhost. Edit the mounted
container example's scope lists for your deployment, preferably using a private
copy of the example. Do not commit credentials or personal configuration.

## Host collection

```bash
cp config/collector.example.json config/local.collector.json
dist/orchard collect -config config/local.collector.json
```

The collector generates a stable random identity in id_file. It reports every
15 seconds, using cheap CPU sampling every two seconds with interval peaks. It
reports memory, swap, disk, load, uptime and available macOS power/lid information.
No process arguments, environment, credentials or user documents are collected.
No inbound listener or root access is required. Losing the console triggers
bounded backoff; it does not affect runner jobs.

Configure runner_dirs with paths to your own runner installations. Only .runner
registration metadata and .drained are read. Each valid GitHub.com scope/runner ID
is explicitly associated with the reporting host. GitHub Enterprise Server URL
support is not implemented in this version. Ambiguous associations remain unknown.
Set power_status to your installed supervisor's status file or leave it empty.

Install as a login service without changing the build runner:

```bash
python3 macos/collector_service.py install --binary dist/orchard --config config/local.collector.json
python3 macos/collector_service.py status
python3 macos/collector_service.py uninstall
```

The collector runs with lower priority. Its private config is stored with mode
0600 under ~/.local/share/runner-orchard/collector. It requires login, independently
of whether the console itself is running continuously in a cluster. Linux hosts
can run the same `collect` command, including on an ARM64 Pi; use an appropriate
user service manager. No Linux kernel battery/lid inference is attempted.

## Semantics and limits

- Host data becomes stale 45 seconds after the last successful receipt. Client
  clock skew cannot extend freshness. Last-known values stay visible as stale.
- GitHub source errors retain prior snapshots and display an error. Sources are
  stale after 90 seconds without success. A failed API is not an empty fleet.
- Offline runner, stale telemetry and a local drain marker are different facts.
  The console never claims that missing telemetry proves sleep.
- Whole-host CPU is 0–100%; sizes/counters are bytes. Memory definitions follow
  each operating system. Swap In/Out are cumulative counters where available,
  not rates. Compressed-memory counters are not yet exposed.
- In-memory history retains up to 480 reports per host, at most two hours.
  Hosts are capped at 128. Server restart loses this history; live snapshots
  repopulate through polling and collector reports. No disk is written by serve.
- GitHub scans are bounded at 2,000 runners per scope, 20 active/queued runs per
  repository per state, and 1,000 jobs per run. Exceeding a bound produces a
  visible source error. Inventory and jobs share the configured polling interval.
- ARC runners only appear while GitHub reports them. This version does not infer
  pool health or scaled-to-zero state and does not map pods to Kubernetes nodes.
- The browser refreshes every ten seconds. Its no-data messages are intentional,
  not demo data. Host names and job content are rendered as text.

## MCP and HTTP

Connect a Streamable HTTP MCP client to `http://127.0.0.1:8787/mcp`, or the private
deployment URL. No authentication header is needed. For Codex, for example:

```toml
[mcp_servers.runner_orchard]
url = "http://127.0.0.1:8787/mcp"
```

Tools: list_runners, get_runner (`scope#id`), list_hosts, get_host, list_active_jobs,
get_host_history (1–120 minutes) and get_fleet_health. All tools use the same state
as the web UI. None execute commands, modify hosts, control power or cancel jobs.

HTTP endpoints: GET /api/state, GET /api/history/{host-id}?minutes=120,
POST /api/telemetry, GET /metrics and GET /healthz. MCP speaks the official SDK's
protocol, not a custom approximation. /healthz checks the server process, not
GitHub availability; adapter errors are visible in /api/state and MCP.

The server requires an allowed_hosts list and validates browser Origin. Requests
are size/concurrency/rate bounded. Put all routes behind private ingress/network
restrictions. Ingestion has no authentication: an allowed network peer can spoof
telemetry. Reported data is informational and cannot authorize a host operation.
If adding a public reverse proxy later, keep ingestion and metrics private.

## Raspberry Pi / ARM64 Kubernetes

The image workflow publishes `ghcr.io/akayami/runner-orchard` for linux/arm64 and
linux/amd64. Go cross-compiles the static executable; no emulation or separate
database is needed on the Pi. Prefer an immutable commit tag over latest.

The generic chart is charts/runner-orchard. Select a Linux ARM64 node through
nodeSelector, use one replica, and supply configuration and an existing GitHub
Secret reference. The chart has a ClusterIP service and no ingress by default.
Operators provide private routing and their preferred secret management.

With an existing Prometheus Operator, enable serviceMonitor.enabled to retain
exported metrics outside the console. No extra Prometheus instance is installed.
The console currently charts its in-memory buffer; use Grafana/Prometheus for
durable history until the optional history-query adapter is implemented.

Config changes roll the pod through a checksum. Tokens referenced through an
environment Secret need a restart on rotation; change rolloutNonce in Helm/GitOps
configuration to trigger it. Run non-root with read-only root
filesystem; there are no Kubernetes API credentials or host mounts in the pod.
