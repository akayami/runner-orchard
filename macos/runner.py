#!/usr/bin/env python3
"""Install and inspect one organization-scoped GitHub runner per macOS account."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import shutil
import subprocess
import tempfile
import time
import urllib.request

BASE = Path.home() / ".local/share/runner-orchard/runner"
TOOL_PATH = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"


def github(endpoint, method="GET", payload=None):
    command = ["gh", "api", "--method", method, endpoint]
    if payload is not None:
        command += ["--input", "-"]
    result = subprocess.run(command, input=json.dumps(payload) if payload is not None else None,
                            text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"GitHub API {method} {endpoint} failed (check gh authentication and permissions)")
    return json.loads(result.stdout) if result.stdout.strip() else None


def pages(endpoint, key):
    items = []
    for page in range(1, 1000):
        result = github(f"{endpoint}?per_page=100&page={page}")[key]
        items.extend(result)
        if len(result) < 100:
            return items
    raise RuntimeError("GitHub pagination exceeded limit")


def read_config(path):
    config = json.loads(path.read_text())
    return validate_config(config)


def validate_config(config):
    if not isinstance(config, dict):
        raise ValueError("runner configuration must be an object")
    for key in ("organization", "name"):
        if not isinstance(config.get(key), str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", config[key]):
            raise ValueError(f"Invalid {key}")
    if not isinstance(config.get("runner_group"), str) or not config["runner_group"].strip() or any(c in config["runner_group"] for c in "\r\n"):
        raise ValueError("Specify an access-restricted runner_group")
    labels = config.get("labels", [])
    if not isinstance(labels, list) or not labels or any(not isinstance(label, str) or not re.fullmatch(
            r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}", label) for label in labels):
        raise ValueError("Specify valid capability labels")
    if not isinstance(config.get("developer_dir"), str):
        raise ValueError("developer_dir must be an absolute Xcode path")
    developer = Path(config["developer_dir"])
    if not developer.is_absolute() or any(c in str(developer) for c in "\r\n"):
        raise ValueError("developer_dir must be an absolute path without newlines")
    if "apple-build" not in labels:
        raise ValueError("The apple-build routing label is required for draining")
    return config


def environment(config):
    return dict(os.environ, PATH=TOOL_PATH, DEVELOPER_DIR=config["developer_dir"])


def run(config, *args):
    subprocess.run(args, cwd=BASE, env=environment(config), check=True)


def service_path():
    # GitHub versions differ: .service may contain a plist path or a label.
    value = (BASE / ".service").read_text().strip()
    path = Path(value) if value.startswith("/") else Path.home() / "Library/LaunchAgents" / f"{value}.plist"
    if path.parent != Path.home() / "Library/LaunchAgents":
        raise RuntimeError("Unexpected runner service path")
    return path


def service_loaded():
    label = service_path().stem
    return subprocess.run(["launchctl", "print", f"gui/{os.getuid()}/{label}"],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def service(config, action):
    path = service_path()
    if action == "stop":
        if service_loaded():
            subprocess.run(["launchctl", "bootout", f"gui/{os.getuid()}/{path.stem}"], check=True)
    elif action == "start":
        if not service_loaded():
            subprocess.run(["launchctl", "enable", f"gui/{os.getuid()}/{path.stem}"], check=True)
            subprocess.run(["launchctl", "bootstrap", f"gui/{os.getuid()}", str(path)], check=True)


def preflight(config, need_gh=True):
    if platform.system() != "Darwin" or platform.machine() not in ("arm64", "x86_64"):
        raise RuntimeError("Requires an Intel or Apple silicon Mac")
    if os.getuid() == 0:
        raise RuntimeError("Run as the CI login account, never root")
    for tool in (("gh", "xcodebuild") if need_gh else ("xcodebuild",)):
        if not shutil.which(tool):
            raise RuntimeError(f"Missing {tool}")
    if not Path(config["developer_dir"]).is_dir():
        raise RuntimeError("Configured Xcode developer_dir is not installed")
    subprocess.run(["xcodebuild", "-version"], env=environment(config), check=True)
    free = shutil.disk_usage(Path.home()).free // (1024 ** 3)
    if free < 30:
        raise RuntimeError(f"Only {free} GiB free; require at least 30 GiB")
    subprocess.run(["launchctl", "print", f"gui/{os.getuid()}"],
                   check=True, stdout=subprocess.DEVNULL)


def install(config, enrollment=None):
    preflight(config, need_gh=enrollment is None)
    org = config["organization"]
    if enrollment is None:
        groups = pages(f"orgs/{org}/actions/runner-groups", "runner_groups")
        group = next((g for g in groups if g["name"] == config["runner_group"]), None)
        if not group or group["visibility"] != "selected" or group["allows_public_repositories"]:
            raise RuntimeError("Runner group must exist, allow selected repositories only, and exclude public repositories")
    BASE.mkdir(parents=True, exist_ok=True, mode=0o700)
    BASE.chmod(0o700)
    configured = BASE / ".runner"
    newly_registered = not configured.exists()
    if enrollment is not None and not newly_registered:
        raise RuntimeError("Already registered; enrollment never replaces a running worker")
    if configured.exists():
        existing = json.loads(configured.read_text(encoding="utf-8-sig"))
        if (existing["gitHubUrl"].rstrip("/") != f"https://github.com/{org}"
                or existing["agentName"] != config["name"]):
            raise RuntimeError("Existing registration differs; refusing to replace it")
        saved = BASE / "machine-config.json"
        if not saved.exists() or json.loads(saved.read_text()) != config:
            raise RuntimeError("Configuration changed; reconfigure explicitly rather than silently ignoring it")
    else:
        arch = "arm64" if platform.machine() == "arm64" else "x64"
        package = enrollment["package"] if enrollment is not None else next(
            p for p in github(f"orgs/{org}/actions/runners/downloads")
            if p["os"] == "osx" and p["architecture"] == arch)
        if (package.get("os") != "osx" or package.get("architecture") != arch
                or not isinstance(package.get("download_url"), str)
                or not package["download_url"].startswith("https://github.com/actions/runner/releases/download/")
                or not re.fullmatch(r"[a-fA-F0-9]{64}", package.get("sha256_checksum", ""))):
            raise RuntimeError("Untrusted runner package metadata")
        with tempfile.TemporaryDirectory(prefix="apple-runner-") as temp:
            archive = Path(temp) / "runner.tar.gz"
            urllib.request.urlretrieve(package["download_url"], archive)
            if hashlib.sha256(archive.read_bytes()).hexdigest() != package["sha256_checksum"]:
                raise RuntimeError("Runner checksum mismatch")
            subprocess.run(["tar", "xzf", str(archive), "-C", str(BASE)], check=True)
        # Capture tokens; never print config.sh's token-bearing command on failure.
        token = enrollment["registration_token"] if enrollment is not None else github(
            f"orgs/{org}/actions/runners/registration-token", "POST")["token"]
        result = subprocess.run([
            "./config.sh", "--unattended", "--url", f"https://github.com/{org}",
            "--token", token, "--name", config["name"], "--runnergroup", config["runner_group"],
            "--labels", ",".join(config["labels"]), "--work", "_work"],
            cwd=BASE, env=environment(config))
        if result.returncode:
            raise RuntimeError("Runner registration failed")
        # GitHub runner service reads .env; ensure Xcode and tool paths survive login.
        with (BASE / ".env").open("a") as stream:
            stream.write(f"\nDEVELOPER_DIR={config['developer_dir']}\n")
        (BASE / ".path").write_text(TOOL_PATH + "\n")
        (BASE / "machine-config.json").write_text(json.dumps(config, indent=2) + "\n")
    if (BASE / ".drained").exists():
        raise RuntimeError("Runner is drained; use resume explicitly")
    if not (BASE / ".service").exists():
        run(config, "./svc.sh", "install")
    path = service_path()
    settings = plistlib.loads(path.read_bytes())
    if not settings.get("KeepAlive"):
        if not newly_registered:
            require_idle(config)
        service(config, "stop")
        settings.update(KeepAlive=True, ThrottleInterval=10, Nice=10)
        path.write_bytes(plistlib.dumps(settings))
    service(config, "start")
    run(config, "./svc.sh", "status")


def provision_group(config, repositories):
    if not repositories:
        raise ValueError("Specify --repositories with private repository names in this organization")
    org = config["organization"]
    ids = []
    for name in repositories:
        if not re.fullmatch(r"[A-Za-z0-9_.-]+", name):
            raise ValueError("Use repository names without an owner prefix")
        repo = github(f"repos/{org}/{name}")
        if not repo["private"]:
            raise RuntimeError(f"{name} must be private")
        ids.append(repo["id"])
    groups = pages(f"orgs/{org}/actions/runner-groups", "runner_groups")
    group = next((g for g in groups if g["name"] == config["runner_group"]), None)
    if group:
        allowed = pages(f"orgs/{org}/actions/runner-groups/{group['id']}/repositories", "repositories")
        if (group["visibility"] != "selected" or group["allows_public_repositories"]
                or {r["id"] for r in allowed} != set(ids)):
            raise RuntimeError("Existing group policy differs; review it in GitHub before changing access")
    else:
        group = github(f"orgs/{org}/actions/runner-groups", "POST", {
            "name": config["runner_group"], "visibility": "selected",
            "allows_public_repositories": False, "selected_repository_ids": ids})
    print(f"Group {group['name']} (id {group['id']}) restricted to {', '.join(repositories)}")


def registration(config):
    saved = json.loads((BASE / ".runner").read_text(encoding="utf-8-sig"))
    if (saved["gitHubUrl"].rstrip("/") != f"https://github.com/{config['organization']}"
            or saved["agentName"] != config["name"]):
        raise RuntimeError("Local runner registration differs from configuration")
    return f"orgs/{config['organization']}/actions/runners/{saved['agentId']}"


def require_idle(config):
    if github(registration(config))["busy"]:
        raise RuntimeError("Runner is busy; use drain and let the job finish")


def drain(config, timeout):
    endpoint = registration(config)
    (BASE / ".drained").touch(mode=0o600)
    labels = github(endpoint + "/labels")["labels"]
    if any(label["name"] == "apple-build" for label in labels):
        github(endpoint + "/labels/apple-build", "DELETE")
    print("Routing label removed. Waiting for assigned jobs to finish...", flush=True)
    deadline = time.monotonic() + timeout
    idle_checks = 0
    while time.monotonic() < deadline:
        idle_checks = 0 if github(endpoint)["busy"] else idle_checks + 1
        if idle_checks >= 3:
            service(config, "stop")
            print("Runner drained and stopped. Use resume to accept jobs again.")
            return
        time.sleep(10)
    raise RuntimeError("Drain timed out. Routing label remains removed; current job was not interrupted")


def resume(config):
    endpoint = registration(config)
    # config.sh tolerates duplicates of built-in labels; the REST API rejects them.
    defaults = {"self-hosted", "macos", "linux", "windows", "arm", "arm64", "x64"}
    custom = [label for label in config["labels"] if label.lower() not in defaults]
    routing = Path.home() / ".local/share/runner-orchard/power-routing/config.json"
    if routing.is_file() and Path(json.loads(routing.read_text())["root"]) == BASE:
        custom = [label for label in custom if label.lower() != "apple-build"]
    if custom:
        github(endpoint + "/labels", "POST", {"labels": custom})
    service(config, "start")
    (BASE / ".drained").unlink(missing_ok=True)


def uninstall(config, timeout):
    drain(config, timeout)
    token = github(f"orgs/{config['organization']}/actions/runners/remove-token", "POST")["token"]
    path = service_path()
    plist = path.read_bytes()
    marker = (BASE / ".service").read_bytes()
    path.unlink(missing_ok=True)
    (BASE / ".service").unlink(missing_ok=True)
    result = subprocess.run(["./config.sh", "remove", "--unattended", "--token", token],
                            cwd=BASE, env=environment(config))
    if result.returncode:
        path.write_bytes(plist)
        (BASE / ".service").write_bytes(marker)
        raise RuntimeError("Unregistration failed; runner is stopped. Retry after checking GitHub access")
    for name in (".drained", "machine-config.json"):
        (BASE / name).unlink(missing_ok=True)
    import health
    health.uninstall()
    print(f"Unregistered. Build files retained at {BASE}; power service is managed separately.")


def cleanup(config, days, apply):
    if not (BASE / ".drained").exists():
        raise RuntimeError("Drain the runner before cleanup")
    require_idle(config)
    if service_loaded():
        raise RuntimeError("Service must be stopped before cleanup")
    cutoff = time.time() - days * 86400
    for directory in (BASE / "_diag", BASE / "_work/_temp"):
        if (directory.is_symlink() or not directory.exists()
                or not directory.resolve().is_relative_to(BASE.resolve())):
            continue
        # Never traverse directories or symlinks into job-controlled paths.
        for path in directory.iterdir():
            if path.is_symlink() or not path.is_file() or path.stat().st_mtime >= cutoff:
                continue
            print(f"{'Removing' if apply else 'Would remove'} {path}")
            if apply:
                path.unlink()


def status(config):
    remote = github(registration(config))
    print(json.dumps({"name": remote["name"], "status": remote["status"], "busy": remote["busy"],
                      "drained": (BASE / ".drained").exists(),
                      "free_gib": shutil.disk_usage(Path.home()).free // 1024 ** 3,
                      "labels": [label["name"] for label in remote["labels"]]}, indent=2))
    power = Path("/Library/Application Support/RunnerOrchard/status.json")
    if power.exists():
        data = json.loads(power.read_text())
        data["stale"] = time.time() - data["updated"] > 15
        print("Power service:", json.dumps(data, indent=2))
    else:
        print("Power service not installed; continuous closed-lid operation is not enabled")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["doctor", "provision-group", "install", "status", "start", "stop",
                                              "drain", "resume", "cleanup", "uninstall"])
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--repositories", nargs="+")
    parser.add_argument("--timeout", type=int, default=7200)
    parser.add_argument("--days", type=int, default=7)
    parser.add_argument("--apply", action="store_true", help="Apply cleanup; otherwise list files only")
    args = parser.parse_args()
    try:
        config = read_config(args.config)
        if args.timeout < 30 or args.days < 1:
            raise ValueError("timeout must be >=30 seconds and days >=1")
        if os.getuid() == 0:
            raise RuntimeError("Runner operations must run as the regular CI account")
        if args.command == "install":
            install(config)
        elif args.command == "provision-group":
            provision_group(config, args.repositories)
        elif args.command == "status":
            status(config)
        elif args.command == "drain":
            drain(config, args.timeout)
        elif args.command == "resume":
            resume(config)
        elif args.command == "uninstall":
            uninstall(config, args.timeout)
        elif args.command == "cleanup":
            cleanup(config, args.days, args.apply)
        elif args.command == "doctor":
            preflight(config)
            subprocess.run(["pmset", "-g", "batt"], check=True)
            print("Local preflight passed. Lid behavior and organization permissions require separate checks.")
        else:
            if args.command == "stop":
                raise RuntimeError("Use drain to stop without interrupting builds")
            if (BASE / ".drained").exists():
                raise RuntimeError("Runner is drained; use resume")
            service(config, args.command)
    except (ValueError, KeyError, OSError, RuntimeError, subprocess.SubprocessError) as error:
        # Never include subprocess command arguments: these can contain registration tokens.
        if isinstance(error, subprocess.SubprocessError):
            parser.exit(1, "External command failed; inspect its output above.\n")
        parser.exit(1, f"{error}\n")


if __name__ == "__main__":
    main()
