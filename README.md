# Runner Orchard

Put spare Macs to work as GitHub Actions runners for iOS and macOS projects.

Runner Orchard provides runner setup and maintenance, local health checks, and
experimental AC-only power management for Intel and Apple silicon Macs, plus a
lightweight web console, outbound host collector and read-only MCP endpoint.
See [console setup](docs/console.md) and the [development plan](docs/development-plan.md).

## Project status

Early-stage, source-first tooling. Runner lifecycle and power-management code is
available today alongside the initial console/MCP implementation. History is
bounded and in memory; no database is required. Optional Kubernetes discovery and
durable-history querying remain future adapters.
This project is not affiliated with Apple or GitHub.

## Quick start

Install Xcode, accept its license and complete first launch. Use a dedicated
standard macOS account and log into its graphical session. From this checkout:

```bash
bash install.sh --tools
```

The wizard asks **standalone or Orchard first**. Standalone asks for organization,
name, group, labels and Xcode locally. Orchard asks for the controller/console URL,
suggested name and Xcode; approve the matching request code and choose its profile
and final name in the console. No personal GitHub token is sent to an Orchard Mac.

```bash
bash install.sh http://orchard.example.test --tools
# JSON skips local configuration prompts:
bash install.sh --config config/local.setup.json --tools
```

Copy either config/setup.standalone.example.json or setup.orchard.example.json
to your private local setup file. The old flat runner JSON also works and selects
standalone mode. `--configure-only` saves private configuration without enrolling
or installing. `--tools` installs only missing tools and bootstraps Homebrew/Python
when needed. Xcode installation/license and macOS privacy consent remain OS steps.
Power management is optional and off unless selected. JSON mode fails rather than
asking for missing values/authentication; pre-authenticate gh in standalone mode
and arrange sudo authorization if enabling power management unattended.

Orchard provisioning is opt-in on the server; see [setup modes](docs/setup.md).
GitHub still dispatches jobs directly. After registration, console outages do not
stop builds. The existing runner and collector commands remain available under
macos/; macos/install.sh forwards to the root installer for compatibility.

In standalone mode the GitHub identity needs permission to manage organization runners and groups.
For a classic CLI token this may require `gh auth refresh -h github.com -s admin:org`.
Use the narrowest permissions supported by your authentication method. The
installer checks GitHub's published runner checksum before extracting it.

Setup manages one Orchard worker per login account. Start with one worker per
physical Mac. Custom workers can coexist: opt in when prompted or set
`allow_existing_workers=true` in setup JSON. Existing workers stay untouched;
see [coexistence and shared power management](docs/setup.md#existing-workers-and-coexistence).
The group must allow only selected private repositories and exclude
public repositories. An existing group with a different access policy is refused.
The Runner Orchard repository being public does not make untrusted public PRs
safe to execute on a persistent machine.

Select your workers from application workflows:

```yaml
runs-on: [self-hosted, macOS, ARM64, apple-build]
```

Use `X64` for Intel workers. Omit architecture only when a job is verified to work
on either host type. Host architecture and build target are different: an Intel
Mac can cross-compile ARM64/universal macOS binaries with compatible Xcode and
dependencies; native ARM64 execution requires Apple silicon.

## Operation

From `macos/`, with your local config:

```bash
python3 runner.py status --config configs/local.json
python3 health.py
python3 runner.py drain --config configs/local.json
python3 runner.py cleanup --config configs/local.json --days 7
python3 runner.py resume --config configs/local.json
```

Drain removes the required `apple-build` label, waits for assigned work, and stops
the service. All pool jobs must request that label. Direct stop is refused.
Cleanup defaults to preview, requires a stopped/drained worker, and only considers
old regular files directly under its diagnostics and temporary directories.
Pass `--apply` to delete those listed files. Workspaces and global caches remain.

The worker lives in `~/.local/share/runner-orchard/runner`. The login service has
automatic restart and lower scheduling priority; builds can still consume
substantial CPU and memory, especially during simulator startup. Workflows should
also bound compiler/test concurrency on machines used interactively.

## Sleep, lid closure and restart

The optional root-owned native power service keeps the machine awake only while
on AC and the configured user has a graphical session. It restores ordinary sleep
on battery, logout, pause or clean termination. The build runner runs as the
regular user; no persistent Homebrew interpreter runs as root.

This service uses the undocumented `pmset disablesleep` setting for monitor-free
closed-lid operation. It is experimental and must be qualified on each machine.
Pause before carrying the laptop or putting it away. Keep active hardware
ventilated. Other power utilities may conflict with its policy.

```bash
bash power-service.sh status
sudo /bin/bash power-service.sh pause
sudo /bin/bash power-service.sh resume
```

The **power service starts at boot; the build runner requires login**. FileVault
unlock and graphical login after a restart are separate recovery steps. No
automatic login, FileVault changes or passwordless sudo are installed. Boot-only
build execution is future work.

## Qualification and removal

```bash
cd macos
python3 -m unittest discover -s . -p 'test_*.py' -v
python3 validate_builds.py
python3 closed_build.py --output evidence/closed-build
python3 power_probe.py --seconds 600 --output evidence/power-cycle
```

Build checks create disposable iOS and macOS SwiftUI projects. The closed-build
check waits for actual lid closure before starting them. Power-cycle qualification
requires unplugging and reconnecting AC while the lid is open. Evidence is ignored
by git and may contain device inventory. Follow qualification with a real project,
signing/UI tests, network recovery and an overnight soak before unattended use.

`power_trial.py` is a bounded experiment for machines without the supervisor;
do not run it alongside an active production sleep override.

To uninstall:

```bash
python3 runner.py uninstall --config configs/local.json
sudo /bin/bash power-service.sh uninstall
```

Build files are retained for review. To replace an installation from another
project, drain and unregister the old worker and explicitly migrate its health
and power services. Alternatively, opt into coexistence during setup. Use one
power supervisor per Mac and distinct directories/names for parallel workers.

## Development

Python uses only the standard library (Python 3.10+). Native power management needs
macOS, Xcode and Swift. Linux CI runs unit tests without touching live runners or
power settings; manual macOS CI compiles Swift. Configuration and credentials stay
outside tracked source. See [contributing](CONTRIBUTING.md), [security](SECURITY.md)
and the [MIT license](LICENSE).
