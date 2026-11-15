# Security

Report suspected vulnerabilities through this repository's private vulnerability
reporting feature when available. Do not include working credentials, personal
logs or private infrastructure details in public issues.

Persistent self-hosted runners execute workflow code with their account's access.
Use a dedicated standard account, trusted private repositories and restricted
runner groups. Never schedule untrusted fork PRs on a personal or shared host.
The root power helper must remain root-owned and separate from job execution.

The monitoring/MCP service is read-only and unauthenticated
for private networks; deployment must not expose ingestion or monitoring data
publicly by default. A reverse proxy's authentication is a deployment decision.
Optional console provisioning adds operator-approved initial runner registration.
Enable it only on a trusted private operator network, with a GitHub credential
permitted to register organization runners. New-machine credentials are transient,
single-claim and separate from telemetry. Never publish provisioning routes or
issue bootstrap credentials across an untrusted plaintext network.

No stable release or formal support window is declared yet. Check open issues and
review native power changes before unattended deployment.
