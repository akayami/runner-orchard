# Runner Orchard development plan

Status: runner tooling available; console, collector and MCP implementation planned.

## Purpose

Make spare Intel and Apple silicon Macs useful as self-hosted GitHub Actions
workers, with clear visibility into runner state and host load. Keep setup and
operation lightweight enough for a laptop used interactively.

The reusable core belongs here. Organization-specific machine configuration,
secrets, deployment domains and ArgoCD Applications belong in operators' own
infrastructure repositories. No public feature should depend on one organization,
application, cluster, private checkout or developer's CLI session.

## Architecture

One Go executable will serve embedded HTML/CSS/JavaScript, a read-only JSON API,
Streamable HTTP MCP and Prometheus metrics. A small unprivileged collector will
send outbound host telemetry. Avoid a frontend framework, Node runtime, database,
separate frontend pod and inbound listener on each laptop.

Standalone operation must work with an executable or container and bounded
in-memory history. Prometheus adds durable history; Kubernetes discovery adds ARC
runner-to-node associations. Both are optional adapters. A generic Helm chart is
an additional package, not a prerequisite for running the console.

Default web/MCP mode is unauthenticated, read-only and private to LAN/VPN. Bind
standalone service to loopback by default; expose it deliberately through an
operator-controlled private ingress. No authentication or Cloudflare dependency
is built into v1. Server-side GitHub API credentials are still required to read
private inventory. Never deliver them to browsers, collectors or MCP callers.

## Data and identity

Poll explicitly configured organization/repository scopes for runners and active
workflow jobs. Use confirmed scope and runner ID to correlate jobs and hosts;
names alone are insufficient across scopes and recreated registrations. For
queued jobs, show that no runner is assigned. Preserve unknown associations.

Collectors generate a stable local host ID and report display name, OS, host
architecture and verified toolchain capabilities separately. Reading non-secret
runner registration metadata is allowed; reading credentials is not. Multiple
runners may map to one host. Missing telemetry is unavailable, not zero.

Record source-specific timestamps and health. Server receipt time determines
heartbeat freshness, avoiding client clock skew. GitHub offline, host telemetry
stale, locally drained, service stopped and API failure are distinct states.
Retain stale last-known data on errors. A disconnected collector does not prove
sleep, and a running local process does not prove GitHub connectivity.

ARC runners are ephemeral. Only controller evidence establishes scaled-to-zero
state; disappearing runners must not generate invented host failures. Inspect
installed controller schemas before implementing exact runner/pod/node joins.

## First web interface

- Fleet counts and filters for state, scope, platform and pool.
- Runner rows with labels, host mapping, active job, elapsed time and GitHub links.
- Host cards and detail views for CPU/core count, normalized load, physical and
  compressed memory where supported, swap usage/activity, free disk and uptime.
- Power source, battery, lid state and local power-service health where supported.
- Recent history and interval peaks to expose short simulator/compilation bursts.
- Clear last-seen indicators, adapter errors and unsupported metrics.

Process details are optional and bounded: executable name, PID, CPU and memory
only. Never collect command arguments, environment variables, user documents,
private logs or hardware serial numbers. Escape all displayed host/job text.

## Collector and performance

Evaluate a small Go collector using platform adapters, initially macOS Intel and
Apple silicon. Read power/health status without changing it. Reuse existing
node-exporter metrics on Linux before installing another agent; Windows is later.

Sample cheap CPU/memory counters every two seconds, aggregate and report every
15 seconds, including peaks. Disk/power can use a lower cadence. Do not routinely
run top/spindump or repeatedly scan every process. Default to low priority, bounded
queues, timeout/backoff and limited concurrency. Collection failure must not
block or fail a build.

Targets to measure on real hardware:

- Collector: less than 1% of one CPU core averaged over five minutes and 30 MiB RSS.
- Console: less than 64 MiB steady-state RSS for a small fleet; initial pod limits
  of 250m CPU / 128 MiB, adjusted from measurements.
- Inventory polling every 30 seconds; active jobs every 15 seconds only when
  needed, with pagination, ETags, jitter and rate-limit backoff.
- Two hours of bounded in-memory history by default; limits on hosts, payload
  size, query windows, response sizes and concurrent remote requests.

Use a documented 0–100% whole-host CPU convention. Document metrics that can
exceed 100% per process. Treat unsupported counters as unavailable. Do not attach
unbounded job URLs/run identifiers to Prometheus series.

## API and MCP

UI, JSON and MCP will use one typed snapshot/query layer so they agree about
status, freshness, units and identity. Proposed MCP tools:

- list_runners and get_runner
- list_hosts and get_host
- list_active_jobs
- get_host_history
- get_fleet_health

Use the official MCP SDK with Streamable HTTP. Test initialization, tool listing,
structured calls, errors and compatibility with a real client. History requests
accept named metrics and bounded time windows, not arbitrary PromQL passthrough.

No shell execution, cancellation, drain, power changes, arbitrary file access or
runner controls are exposed in v1. Apply request limits and Origin/Host validation
where appropriate. Ingestion is private and validates schema/host IDs. With no
ingestion authentication, other allowed network clients could spoof reports; such
reports must never authorize execution or control operations.

## Access and packaging

Support an operator-supplied GitHub token and a dedicated read-only GitHub App
with short-lived installation tokens. Verify API permissions through integration
tests. Do not reuse a controller's write credential by default. Optional ARC
discovery gets only the necessary namespaced read permissions; no cluster-admin,
secret enumeration, privileged DaemonSet or host filesystem mounts.

Build collector binaries for Darwin arm64/amd64 and server images for Linux
arm64/amd64. Package examples for standalone execution, Docker/Compose and Helm.
The core must not require a specific DNS domain, registry, monitoring stack or
secret manager. Version the telemetry schema and expose capabilities.

Install Mac collection as an unprivileged user LaunchAgent with uninstall and
health reporting. Preserve operator hooks and existing installations. Never
silently replace service identities, register duplicate workers or install a
second power supervisor. Boot-without-login build execution is a separate feature;
FileVault unlock is a separate requirement.

## Implementation milestones

1. **Portable foundation:** extracted runner lifecycle/power tools, MIT license,
   neutral examples, fresh public history, clean CI and reproducible checks.
2. **Inventory:** typed config/snapshots, GitHub polling and read-only API with
   pagination, backoff, source freshness and active-job correlation.
3. **Host telemetry:** Mac collector, explicit identity mapping, power/health
   integration, bounded history and measured idle/build/simulator overhead.
4. **Console and MCP:** embedded responsive UI, the shared query layer, real MCP
   client tests and optional Prometheus history. No fake live data in demos.
5. **Optional Kubernetes adapter:** existing node metrics and exact ARC mappings;
   validate scale-to-zero, pending pods and unmapped runner states.
6. **Distribution:** generic Helm/Compose examples, versioned multiarch artifacts,
   checksums, upgrade/uninstall instructions and clean install on another host.
7. **Qualification:** overnight observation, lid closure, battery transitions,
   restart/login, loss of network and API errors. Compare metrics with host tools.

Tests cover scope collisions, stale heartbeats, unknown mappings, rate limits,
malformed reports, memory/history bounds, unit conventions, HTML escaping, MCP
protocol behavior and missing-adapter states. Run builds/tests with limited
parallelism on interactive hosts. Hardware behavior cannot be established solely
through mocks. Report the exact tested OS/toolchain combinations.

## Later decisions

Project naming/license are settled: Runner Orchard, MIT. Before stable releases,
choose support windows and verify both Intel and Apple silicon machines. Intel
hosts may cross-compile ARM64 artifacts while requiring different hardware for
native execution; capabilities must reflect actual SDK/dependency compatibility.

Deferred: authenticated/public hosting, control tools, live log streaming,
automatic load shedding, scheduling policy, VM isolation, Windows collection and
login-independent boot workers. Operator deployments can add a reverse proxy
without forcing that infrastructure into the project.
