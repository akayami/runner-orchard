#!/bin/bash
# Compatibility entry point; root install.sh also supports both modes and JSON.
set -euo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
exec /bin/bash "$script_dir/../install.sh" "$@"
