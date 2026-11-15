#!/usr/bin/env python3
"""Build real SwiftUI iOS and macOS apps without signing or persistent artifacts."""
import argparse
import datetime
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="apple-runner-validation-") as temporary:
        root = Path(temporary)
        source = root / "Sources"
        source.mkdir()
        (source / "App.swift").write_text('''import SwiftUI
@main struct RunnerValidationApp: App {
    var body: some Scene { WindowGroup { Text("Apple runner validation") } }
}
''')
        (root / "project.yml").write_text('''name: RunnerValidation
options:
  deploymentTarget:
    iOS: "17.0"
    macOS: "14.0"
settings:
  base:
    GENERATE_INFOPLIST_FILE: YES
    PRODUCT_BUNDLE_IDENTIFIER: io.runner-orchard.validation
    SWIFT_VERSION: "5.0"
targets:
  Validation-iOS:
    type: application
    platform: iOS
    sources: [Sources]
  Validation-macOS:
    type: application
    platform: macOS
    sources: [Sources]
''')
        subprocess.run(["xcodegen", "generate"], cwd=root, check=True)
        for scheme, destination in (("Validation-iOS", "generic/platform=iOS"),
                                    ("Validation-macOS", "platform=macOS")):
            print(f"{datetime.datetime.now().isoformat()} Building {scheme}", flush=True)
            subprocess.run(["xcodebuild", "-quiet", "-project", "RunnerValidation.xcodeproj",
                            "-scheme", scheme, "-destination", destination,
                            "-derivedDataPath", str(root / f"DerivedData-{scheme}"),
                            "CODE_SIGNING_ALLOWED=NO", "-jobs", "2", "build"], cwd=root, check=True)
        print(f"{datetime.datetime.now().isoformat()} All Apple build checks passed", flush=True)


if __name__ == "__main__":
    main()
