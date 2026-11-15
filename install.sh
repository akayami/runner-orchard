#!/bin/bash
# Bootstrap Python if needed; all configuration/installation logic lives in setup.py.
set -euo pipefail
if [[ $EUID == 0 ]]; then echo 'Run as the regular macOS login user, not root.' >&2; exit 1; fi
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tools=0
for argument in "$@"; do if [[ "$argument" == --tools ]]; then tools=1; fi; done
if ! command -v python3 >/dev/null || ! python3 -c 'import sys; sys.exit(sys.version_info < (3, 10))'; then
  if [[ $tools != 1 ]]; then echo 'Python 3.10+ is required. Re-run with --tools to install it.' >&2; exit 1; fi
  if ! command -v brew >/dev/null; then
    echo 'Installing Homebrew using its official installer (it may request sudo).'
    installer=$(mktemp -t orchard-homebrew)
    trap 'rm -f "$installer"' EXIT
    /usr/bin/curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh -o "$installer"
    /bin/bash "$installer"
  fi
  brew install python
fi
if [[ -n ${installer:-} ]]; then rm -f "$installer"; trap - EXIT; fi
exec python3 "$script_dir/macos/setup.py" "$@"
