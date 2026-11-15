#!/usr/bin/env python3
"""Configure/install a standalone runner or request provisioning from Orchard."""
import argparse
import getpass
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

import health
import runner

ROOT = Path(__file__).resolve().parents[1]
MACOS = ROOT / "macos"


def console_url(value):
    if not isinstance(value, str) or any(c in value for c in "\r\n"):
        raise ValueError("console_url must be an HTTP(S) origin")
    u = urllib.parse.urlsplit(value.strip())
    try:
        _ = u.port
    except ValueError:
        raise ValueError("invalid console port") from None
    if (u.scheme not in ("http", "https") or not u.hostname or u.username or u.password
            or u.path not in ("", "/") or u.query or u.fragment):
        raise ValueError("use the console base URL, without credentials or /api paths")
    return urllib.parse.urlunsplit((u.scheme, u.netloc, "", "", ""))


def machine_name():
    name = socket.gethostname().split(".")[0]
    return re.sub(r"[^A-Za-z0-9_-]", "-", name).strip("-_")[:58] + "-apple"


def xcode_path():
    result = subprocess.run(["xcode-select", "-p"], capture_output=True, text=True)
    path = result.stdout.strip()
    return path if result.returncode == 0 and path.endswith("/Contents/Developer") else "/Applications/Xcode.app/Contents/Developer"


def ask(label, default="", required=True):
    while True:
        value = input(f"{label}" + (f" [{default}]" if default else "") + ": ").strip() or default
        if value or not required:
            return value
        print("A value is required.")


def existing_workers():
    """Discover registrations in the supported legacy layout, without reading credentials."""
    legacy = Path.home() / ".local/share/github-runners"
    return sorted(path.parent for path in legacy.glob("*/.runner") if path.is_file())


def check_existing_workers(cfg):
    workers = existing_workers()
    if workers and not cfg.get("allow_existing_workers", False):
        raise RuntimeError("Existing workers detected; rerun the wizard to choose coexistence, "
                           "or set allow_existing_workers=true in setup JSON. Migration is optional")
    if workers and cfg["power_management"]:
        raise RuntimeError("For coexistence, set power_management=false and manage host-wide "
                           "power separately. Setup will not install another power supervisor")


def interactive(url_hint=""):
    print("Runner Orchard setup\n1. Standalone — configure/register directly with GitHub\n2. Orchard — provision from a console and report metrics")
    while True:
        mode = ask("Mode (1 or 2)", "2" if url_hint else "1")
        if mode in ("1", "2"):
            break
        print("Choose 1 or 2.")
    cfg = {"mode": "orchard" if mode == "2" else "standalone"}
    if mode == "2":
        while True:
            try:
                cfg["console_url"] = console_url(ask("Orchard controller/console URL", url_hint))
                break
            except ValueError as error:
                print(error)
        cfg["name"] = ask("Suggested runner name (editable in console)", machine_name())
        cfg["developer_dir"] = ask("Xcode developer directory", xcode_path())
        print("Organization, group and labels will come from the profile approved in the console.")
    else:
        org = ask("GitHub organization")
        name = ask("Unique runner name", machine_name())
        group = ask("Runner group", "apple-builds")
        extras = ask("Extra capability labels, comma-separated (apple-build is automatic)", required=False)
        cfg["runner"] = {"organization": org, "name": name, "runner_group": group,
                         "labels": list(dict.fromkeys(["apple-build"] + [x.strip() for x in extras.split(",") if x.strip()])),
                         "developer_dir": ask("Xcode developer directory", xcode_path())}
        repos = ask("Private repository names for a NEW group, comma-separated (blank joins existing)", required=False)
        cfg["private_repositories"] = [r.strip() for r in repos.split(",") if r.strip()]
    workers = existing_workers()
    if workers:
        print("Existing workers:\n" + "\n".join(f"  {path}" for path in workers))
        print("Each worker can run a job at the same time, sharing this Mac's CPU, memory and signing keychains.")
        cfg["allow_existing_workers"] = ask("Install Orchard alongside these workers? (y/N)", "n").lower() in ("y", "yes")
        if not cfg["allow_existing_workers"]:
            raise RuntimeError("Setup cancelled; existing workers were left untouched")
        cfg["power_management"] = False
        print("Existing workers will be left untouched. Host-wide power management must be managed separately.")
    else:
        cfg["power_management"] = ask("Enable experimental AC-only power service? (y/N)", "n").lower() in ("y", "yes")
    return normalize(cfg)


def normalize(raw):
    if not isinstance(raw, dict):
        raise ValueError("setup JSON must be an object")
    # Preserve the original flat runner JSON interface.
    if "organization" in raw:
        raw = {"mode": "standalone", "runner": raw}
    allowed = {"mode", "runner", "console_url", "name", "developer_dir", "power_management", "install_tools", "collector_binary", "private_repositories", "allow_existing_workers"}
    if set(raw) - allowed:
        raise ValueError("unknown setup configuration fields")
    mode = raw.get("mode")
    if mode not in ("standalone", "orchard"):
        raise ValueError("mode must be standalone or orchard")
    result = dict(raw)
    for field in ("power_management", "install_tools", "allow_existing_workers"):
        if not isinstance(result.get(field, False), bool):
            raise ValueError(f"{field} must be true or false")
    result.setdefault("power_management", False)
    result.setdefault("install_tools", False)
    result.setdefault("allow_existing_workers", False)
    if mode == "standalone":
        runner.validate_config(result.get("runner"))
        if result.get("console_url"):
            raise ValueError("choose orchard mode to use console provisioning")
        repos = result.get("private_repositories", [])
        if not isinstance(repos, list) or any(not isinstance(r, str) or not re.fullmatch(r"[A-Za-z0-9_.-]+", r) for r in repos):
            raise ValueError("private_repositories must be a list of repository names")
    else:
        result["console_url"] = console_url(result.get("console_url"))
        name = result.get("name", machine_name())
        developer = result.get("developer_dir", "/Applications/Xcode.app/Contents/Developer")
        runner.validate_config({"organization": "example", "name": name, "runner_group": "example", "labels": ["apple-build"], "developer_dir": developer})
        result.update(name=name, developer_dir=developer)
        if "runner" in result:
            raise ValueError("organization/group/labels are chosen by the console in orchard mode")
        binary = result.get("collector_binary", "")
        if not isinstance(binary, str):
            raise ValueError("collector_binary must be a path")
    return result


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".orchard-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(value, stream, indent=2)
            stream.write("\n")
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def tools(cfg, noninteractive):
    formulas = {"xcodegen": "xcodegen", "xcbeautify": "xcbeautify", "fastlane": "fastlane"}
    if cfg["mode"] == "standalone":
        formulas["gh"] = "gh"
    elif not cfg.get("collector_binary"):
        formulas["go"] = "go"
    missing = [formula for command, formula in formulas.items() if not shutil.which(command)]
    if not missing:
        return
    if not cfg["install_tools"]:
        raise RuntimeError("Missing tools: " + ", ".join(missing) + ". Re-run with --tools.")
    if not shutil.which("brew"):
        print("Installing Homebrew using its official installer; sudo may be required.")
        with tempfile.TemporaryDirectory(prefix="orchard-brew-") as tmp:
            installer = Path(tmp) / "install.sh"
            urllib.request.urlretrieve("https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh", installer)
            env = dict(os.environ)
            if noninteractive:
                env["NONINTERACTIVE"] = "1"
            subprocess.run(["/bin/bash", str(installer)], env=env, check=True)
    env = dict(os.environ, HOMEBREW_NO_AUTO_UPDATE="1")
    subprocess.run(["brew", "install", *missing], env=env, check=True)


def api(url, payload, secret=None):
    headers = {"Content-Type": "application/json"}
    if secret:
        headers["Authorization"] = "Bearer " + secret
    request = urllib.request.Request(url, data=json.dumps(payload).encode(), headers=headers, method="POST")
    # Never redirect enrollment credentials to another origin.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, hdrs, newurl):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=60) as response:
            data = response.read(1024 * 1024 + 1)
            if len(data) > 1024 * 1024:
                raise RuntimeError("console response too large")
            return json.loads(data)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"Console enrollment failed (HTTP {error.code}); check whether provisioning is enabled and approved") from None
    except urllib.error.URLError:
        raise RuntimeError("Console unavailable; check the URL, DNS and local-network permission") from None


def enroll(cfg):
    secret = secrets.token_hex(32)
    arch = "arm64" if platform.machine() == "arm64" else "x64"
    request = api(cfg["console_url"] + "/api/enrollment", {"name": cfg["name"], "architecture": arch,
                  "developer_dir": cfg["developer_dir"], "secret": secret})
    identifier = request.get("id", "")
    if not re.fullmatch(r"[a-f0-9]{24}", identifier):
        raise RuntimeError("Invalid console enrollment identifier")
    print(f"Open {cfg['console_url']} and approve this machine. Compare request code: {identifier}", flush=True)
    deadline = time.monotonic() + 600
    while time.monotonic() < deadline:
        result = api(cfg["console_url"] + f"/api/enrollment/{identifier}/claim", {}, secret)
        if result.get("status") == "ready":
            runner.validate_config(result.get("runner"))
            if not isinstance(result.get("registration_token"), str) or not result["registration_token"]:
                raise RuntimeError("Console did not supply a registration credential")
            return result
        if result.get("status") != "pending":
            raise RuntimeError("Enrollment is no longer pending; start a new request")
        time.sleep(3)
    raise RuntimeError("Enrollment expired; run setup again to request approval")


def install(cfg, paths, noninteractive):
    if platform.system() != "Darwin" or platform.machine() not in ("arm64", "x86_64") or os.getuid() == 0:
        raise RuntimeError("install as a regular macOS login user on Intel or Apple silicon")
    if cfg["mode"] == "orchard" and (runner.BASE / ".runner").exists():
        raise RuntimeError("Runner is already registered. Enrollment never replaces an existing worker")
    check_existing_workers(cfg)
    tools(cfg, noninteractive)
    enrollment = None
    collector_binary = None
    if cfg["mode"] == "standalone":
        auth = subprocess.run(["gh", "auth", "status", "--hostname", "github.com"], capture_output=True)
        if auth.returncode:
            if noninteractive:
                raise RuntimeError("GitHub authentication required; run gh auth login before JSON setup")
            subprocess.run(["gh", "auth", "login", "--hostname", "github.com", "--web", "--scopes", "admin:org"], check=True)
        machine = cfg["runner"]
        if cfg.get("private_repositories"):
            runner.provision_group(machine, cfg["private_repositories"])
    else:
        placeholder = {"organization": "example", "name": cfg["name"], "runner_group": "example", "labels": ["apple-build"], "developer_dir": cfg["developer_dir"]}
        runner.preflight(placeholder, need_gh=False)
        collector_binary = Path(cfg.get("collector_binary") or ROOT / ".build/orchard").expanduser().resolve()
        if not cfg.get("collector_binary"):
            collector_binary.parent.mkdir(parents=True, exist_ok=True)
            subprocess.run(["/usr/bin/nice", "-n", "10", "go", "build", "-p", "2", "-trimpath", "-o", str(collector_binary), "./cmd/orchard"],
                           cwd=ROOT, env=dict(os.environ, GOMAXPROCS="2", CGO_ENABLED="0"), check=True)
        if not collector_binary.is_file() or not os.access(collector_binary, os.X_OK):
            raise RuntimeError("collector_binary must be an executable native Orchard binary")
        enrollment = enroll(cfg)
        machine = enrollment["runner"]
        save(paths["runner"], machine)
    runner.install(machine, enrollment=enrollment)
    if cfg["power_management"]:
        build = MACOS / ".build"
        build.mkdir(exist_ok=True)
        binary = build / "apple-runner-power"
        subprocess.run(["xcrun", "swiftc", "-swift-version", "5", "-O", str(MACOS / "ApplePower.swift"), "-o", str(binary)], check=True)
        command = ["sudo"] + (["-n"] if noninteractive else []) + ["/bin/bash", str(MACOS / "power-service.sh"), "install", str(binary), getpass.getuser()]
        subprocess.run(command, check=True)
    health.install(require_power=cfg["power_management"])
    if cfg["mode"] == "orchard":
        collector = {"endpoint": cfg["console_url"] + "/api/telemetry", "name": machine["name"],
                     "id_file": "~/.local/share/runner-orchard/collector.id", "runner_dirs": [str(runner.BASE)],
                     "power_status": "/Library/Application Support/RunnerOrchard/status.json" if cfg["power_management"] else ""}
        save(paths["collector"], collector)
        subprocess.run([os.sys.executable, str(MACOS / "collector_service.py"), "install", "--binary", str(collector_binary), "--config", str(paths["collector"])], check=True)
    print("Runner installed. GitHub assigns jobs directly; console availability is not required for builds.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", nargs="?", help="Console URL or JSON setup file")
    parser.add_argument("--config", type=Path, help="JSON configuration; skips all local configuration prompts")
    parser.add_argument("--tools", action="store_true", help="Install missing command-line tools")
    parser.add_argument("--configure-only", action="store_true", help="Save configuration without installing/enrolling")
    parser.add_argument("--output-dir", type=Path, default=MACOS / "configs")
    args = parser.parse_args()
    try:
        file = args.config
        hint = ""
        if args.source:
            if args.source.startswith(("http://", "https://")):
                hint = console_url(args.source)
            else:
                if file:
                    raise ValueError("provide one JSON configuration file")
                file = Path(args.source)
        if file:
            cfg = normalize(json.loads(file.read_text()))
            if hint:
                if cfg["mode"] != "orchard":
                    raise ValueError("a console URL requires orchard mode")
                cfg["console_url"] = hint
        else:
            cfg = interactive(hint)
        cfg["install_tools"] = cfg["install_tools"] or args.tools
        paths = {"setup": args.output_dir / "local.setup.json", "runner": args.output_dir / "local.json", "collector": args.output_dir / "local.collector.json"}
        save(paths["setup"], cfg)
        if cfg["mode"] == "standalone":
            save(paths["runner"], cfg["runner"])
        print(f"Mode: {cfg['mode']}. Private setup configuration: {paths['setup']}")
        if args.configure_only:
            return 0
        install(cfg, paths, noninteractive=file is not None)
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError, EOFError) as error:
        # Subprocess arguments can include transient credentials: do not echo them.
        text = "External installer command failed; inspect its output above" if isinstance(error, subprocess.SubprocessError) else str(error)
        parser.exit(1, text + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
