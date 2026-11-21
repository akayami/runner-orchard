# Windows x64 worker

The runner uses an automatic Windows supervisor service and a demand-start
GitHub Actions service. Both use separate Windows virtual service accounts.
The supervisor stores its credential and pause state under an ACL-protected
`ProgramData\RunnerOrchard` directory so startup is independent of desktop
sign-in. The tray app is optional and starts in each signed-in user's session.

## Console and enrollment

Deploy Orchard with a trusted HTTPS certificate and a Windows provisioning
profile. Set `os` to `win` and use a `windows-builds` group restricted to the
selected private repositories. Workflows for this group must require
`windows-build`. Keep the existing Windows VM on its own route.

Read the DevOps secret inventory and verify vault metadata before allocating
credentials. Prepare a machine-specific GCP Secret Manager entry for the
runner identity backup and a distinct entry for a 32-byte random lifecycle
credential. Deliver the latter as 64 hex characters in a restricted file.
The console config gets only the SHA-256 digest of the decoded bytes.

From the Orchard checkout, build `orchard.exe` with
`go build -trimpath -o orchard.exe ./cmd/orchard`. Use elevated Windows
PowerShell for enrollment and activation. Run the configure action first if
you want to preview the location and service names.

```powershell
.\windows\install.ps1 -Action Enroll -ConsoleUrl https://orchard.akayami.home -VaultProject YOUR_PROJECT -RunnerBackupSecret runner-orchard-windows-LAPTOP-backup -InstallTools
```

Approve the request code with the Windows profile. Enrollment verifies the
runner ZIP checksum, registers offline, backs up the machine-bound runner
identity, and prints the numeric runner ID. `-InstallTools` is optional and
installs missing machine-wide Git/LFS, PowerShell, Python, and MSVC/SDK
prerequisites. The reviewed Iron Horse workflow installs the pinned Rust
toolchain for the service account. Complete any reported Windows restart
before worker qualification.

Add the returned `worker_id`, `organization` and `runner_id` as a
`windows_workers` entry in the deployed server config, with the digest in
`token_sha256`. After GitOps synchronizes that binding, activate.

```powershell
.\windows\install.ps1 -Action Activate -ConsoleUrl https://orchard.akayami.home -OrchardExe .\orchard.exe -WorkerTokenFile C:\private\orchard-worker-token -InstallTray
```

Add `-ManageACPower` only if you want Orchard to manage the three AC settings
for closed-lid operation. The installer reports supported sleep states and
captures the current power scheme. During an eligible AC period, the service
sets lid close to Do Nothing and sleep and hibernate timeouts to Never. A
manual pause, loss of AC, or uninstall restores settings still owned by
Orchard. A different plan or independently modified setting stops further
management. Check the console for power warnings and verify the behavior on
the physical laptop, including Modern Standby where applicable.

Keep the original private token delivery file outside Git. The installer
copies it into the supervisor's ACL boundary. If the certificate is signed
by a private CA, install the approved trust chain for the machine so the
supervisor service can validate HTTPS before login.

## Controls and qualification

Use `orchard.exe status`, `orchard.exe pause`, `orchard.exe pause -hours 1.5`,
or `orchard.exe resume`. Local signed-in accounts can use these commands
and the tray app. A new pause replaces the old deadline. Expiry and early
resume recheck eligibility. The service owns persistent state, and an
ordinary battery power transition does not clear a manual pause.

Run Iron Horse's reviewed Orchard Windows qualification workflow on AC after
a cold boot, before anyone signs in. It requires Session 0, runs native source
tests and compilation, and validates offscreen startup of the exact Windows
ZIP. Confirm the laptop's lid state physically and exercise both idle and busy
unplug, timed pause, early resume, remote outage, and recovery. Record any
assigned-job interruption from ordinary battery sleep. Add the release-routing
capability only after this qualification passes.

Use `-Action Uninstall` from elevated Windows PowerShell with the same console
URL. The installer waits for the service to drain, unregisters the offline
runner, and removes the two service registrations and tray startup entry.
The ACL-protected files remain available for explicit cleanup and audit.
