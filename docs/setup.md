# Setup modes and console provisioning

Run `bash install.sh --tools` from a Runner Orchard checkout. The first question
selects standalone or Orchard. Supplying a console URL preselects Orchard; a JSON
file skips local configuration questions entirely. Neither path installs a web
server on the Mac. Both produce an ordinary organization-scoped GitHub runner.

## Standalone

The wizard asks for GitHub organization, unique name, restricted group, additional
capability labels and Xcode path. It can create a new selected-private-repository
group when explicit repository names are provided. Existing group access is never
silently widened. Registration uses the local gh identity; interactive setup can
launch GitHub authentication. JSON mode requires authentication already present.
No console is contacted and no collector is required.

## Orchard

```bash
bash install.sh http://orchard.example.test --tools
```

Choose Orchard, confirm the URL, suggested name and local Xcode path, then compare
the displayed request code with the console's New machines panel. Select an
administrator-configured profile and final runner name there and approve setup.
The installer polls for up to ten minutes. Only that requesting machine can claim
the response, using a one-time random credential that never appears in the console.

The console validates that the target group allows selected private repositories
and excludes public ones, refuses duplicate runner names, obtains a short-lived
registration token and checksum-bearing official runner package metadata, and
returns them once to the approved installer. The long-lived GitHub credential
stays on the console. This handoff is delivered once; GitHub's registration token
is organization-scoped and expires after one hour, rather than being a credential
restricted to one particular runner name. Approve only trusted machines.
See [GitHub's registration API](https://docs.github.com/en/rest/actions/self-hosted-runners#create-a-registration-token-for-an-organization).
The Mac verifies the archive checksum before extraction.
It then registers directly with GitHub and installs the outbound metrics collector.

The organization/group/labels come from a central provisioning profile, not the
telemetry URL or a repository name guessed from the machine. Profiles are configured
in the server JSON or Helm config.provisioning values:

```json
{
  "provisioning": {
    "enabled": true,
    "profiles": [{
      "id": "apple",
      "organization": "your-organization",
      "runner_group": "apple-builds",
      "labels": ["apple-build"]
    }]
  }
}
```

The server's GitHub credential must now permit organization runner registration
and group reads. Provisioning is disabled by default; monitoring credentials need
not be upgraded unless an operator enables it. Host architecture labels are
assigned by GitHub and cannot be supplied as capability labels in a profile.

Provisioning profiles and enrollment requests use GitHub package OS values.
Omitting `os` defaults to `osx` for existing Mac configurations. A Windows
profile uses `"os": "win"`, requires the `windows-build` capability,
and should refer to a separate group restricted to selected private repositories.
Windows requests use `"architecture": "x64"` and omit `developer_dir`.
The console only offers matching profiles and rejects cross-platform approvals
before issuing registration credentials. These settings enable server-side
enrollment once a Windows installer is available.

## Windows supervisor configuration

The Windows supervisor runs as the `RunnerOrchardSupervisor` service with
`orchard.exe supervise -config FILE`. It controls one existing GitHub Actions
runner service. The runner service must use demand start with recovery actions
disabled, and the supervisor identity needs query, start, and stop rights on
that service. Install both under low-privilege identities, with the supervisor
able to inspect the runner worker process. The supervisor config uses absolute
paths:

```json
{
  "runner_service": "actions.runner.example.windows-laptop",
  "runner_dir": "C:\\Orchard\\runner",
  "lifecycle_endpoint": "https://orchard.example.test/api/windows-workers/windows-laptop",
  "token_file": "C:\\Orchard\\private\\worker-token",
  "pause_file": "C:\\Orchard\\private\\pause-until"
}
```

The console's `windows_workers` list binds each `id` to its `organization`,
numeric `runner_id`, and `token_sha256`. Store the 32 random credential bytes
as hex in the private token file and use the SHA-256 digest of the original
bytes in the console binding. The client requires a trusted HTTPS certificate.
The only delegated GitHub change is adding or removing `windows-build` on
that ID. Workflows targeting the group must require this label.

The supervisor removes the routing label before stopping an idle listener.
It waits for sustained idle and checks again immediately before requesting
the service stop. GitHub can assign work before label removal propagates, and
the remote busy report can lag local process state. Battery sleep can still
interrupt assigned work. The Windows installer and local pause controls need
to be provisioned and qualified before using this service on a worker.

New-machine requests are bounded, expire after ten minutes, and are held in RAM.
A console restart invalidates pending requests; rerun setup. Installed runner
identity persists locally and in GitHub, so existing builds do not depend on this
queue or the console. No database is added. Profiles remain operator-controlled
configuration; the UI approves initial name/profile selection, not arbitrary
commands or lifecycle edits to existing machines. MCP tools remain read-only.

This unauthenticated UI assumes a trusted private operator network. Approving
setup is a privileged operation. Do not expose the provisioning routes publicly.
Use HTTPS on networks where bootstrap credentials could be intercepted. The
installer never follows enrollment redirects to another origin. No incoming
listener is installed on the Mac; macOS may ask for outbound local-network access.

## JSON and saved files

Use `--config FILE` or pass a JSON file as the positional argument. Examples are
config/setup.standalone.example.json and config/setup.orchard.example.json.
Standalone JSON contains runner configuration; Orchard JSON contains the URL and
local machine hints. Orchard still requires console approval; "no prompts" means
no local configuration dialogue, not bypassing the operator's approval policy.

Files saved under macos/configs/local*.json are ignored by git and mode 0600.
Registration/enrollment credentials are not saved. The optional collector's
endpoint is derived from console_url plus /api/telemetry. Do not edit the runner's
GitHub registration URL to point at Orchard.

`--tools` installs missing command-line dependencies only. Orchard builds its
collector with limited Go parallelism unless collector_binary supplies a prebuilt
native executable. JSON may set install_tools=true. Full Xcode and its license
cannot be provisioned unattended by this script; Homebrew/sudo and OS privacy
prompts may require prior host preparation. Enabling power_management in JSON uses
noninteractive sudo and fails if authorization is unavailable.

## Existing workers and coexistence

Migration is optional. When setup detects registrations under
`~/.local/share/github-runners/*/.runner`, the wizard lists their directories and
asks whether to install Orchard alongside them. The default is no. JSON skips
this question and requires an explicit top-level `"allow_existing_workers": true`
to proceed when workers are detected. This option works in both setup modes.
Installations in arbitrary custom directories are not automatically discovered.

Existing workers are not drained, reconfigured or removed. Orchard uses its own
installation directory and needs a distinct GitHub runner name and service name.
The coexistence option never permits replacing an existing Orchard registration.
Each worker can accept a job independently, so concurrent builds share CPU,
memory, signing keychains and other machine-wide resources.

Coexistence setup does not install a power supervisor: it uses
`power_management=false`. JSON with detected workers and power_management=true
fails before installation. Manage host-wide power separately and use only one
supervisor for the Mac. Existing power services remain untouched.

To replace an old worker instead, drain and migrate it explicitly. Collector-only
attachment to an existing worker continues to use macos/collector_service.py;
configure its runner_dirs to report additional custom workers on the same host.
