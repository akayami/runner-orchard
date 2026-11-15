#!/bin/bash
# Interactive bootstrap, executed as the CI login user. sudo only for power service.
set -euo pipefail
cd "$(dirname "$0")"
if [[ $EUID == 0 ]]; then echo 'Run as the CI account, not root.' >&2; exit 1; fi
config=${1:?Usage: bash install.sh CONFIG [--tools]}
if [[ ${2:-} == --tools ]]; then
  brew bundle --file Brewfile
fi
python3 runner.py doctor --config "$config"
python3 -m unittest discover -s . -p 'test_*.py'
mkdir -p .build
xcrun swiftc -swift-version 5 -O ApplePower.swift -o .build/apple-runner-power
python3 runner.py install --config "$config"
sudo /bin/bash power-service.sh install "$PWD/.build/apple-runner-power" "$(id -un)"
python3 health.py --install
python3 runner.py status --config "$config"
echo 'Installed. Verify closed-lid builds and battery recovery before leaving unattended.'
