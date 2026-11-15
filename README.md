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
standard macOS account and log into its graphical session. Install Homebrew,
then from this checkout:

```bash
cd macos
brew bundle --file Brewfile
cp configs/example.json configs/local.json
# Edit local.json with your organization, unique runner name, group and Xcode.
gh auth login
python3 runner.py provision-group --config configs/local.json --repositories your-private-app
bash install.sh configs/local.json
```

The GitHub identity needs permission to manage organization runners and groups.
For a classic CLI token this may require `gh auth refresh -h github.com -s admin:org`.
Use the narrowest permissions supported by your authentication method. The
installer checks GitHub's published runner checksum before extracting it.

Setup installs one worker per login account. Start with one account/worker per
physical Mac. The group must allow only selected private repositories and exclude
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

Build files are retained for review. Existing installations from another project
need explicit migration: drain and unregister the old worker, uninstall its health
and power services, then adopt this project. Never run two power supervisors or
accidentally register a second worker on the same laptop.

## Development

Python uses only the standard library (Python 3.10+). Native power management needs
macOS, Xcode and Swift. Linux CI runs unit tests without touching live runners or
power settings; manual macOS CI compiles Swift. Configuration and credentials stay
outside tracked source. See [contributing](CONTRIBUTING.md), [security](SECURITY.md)
and the [MIT license](LICENSE).
