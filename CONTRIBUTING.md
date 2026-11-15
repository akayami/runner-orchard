# Contributing

Keep the reusable core independent of a particular organization, cluster or host.
Use synthetic examples. Do not submit personal machine configs, diagnostics,
hardware inventory, private infrastructure names, addresses, credentials or logs.

Run Python tests from macos/ and shellcheck on changed shell scripts. Compile
ApplePower.swift on macOS when changing native power code; never install a root
service or register a runner as part of an automated unit test. Hardware behavior
requires separate attended qualification, not just mocked tests.

Keep monitoring read-only, bounded and inexpensive. Prefer existing host counters
to high-frequency process scans. Document measured resource use and behavior when
GitHub, the collector or the network is unavailable.

Contributions are provided under the repository's MIT license. Do not copy code
or assets whose license is incompatible or whose provenance is unclear.
