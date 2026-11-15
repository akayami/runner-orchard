#!/bin/bash
# Install only a compiled native binary into root-owned paths. Run using sudo.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
label=io.runner-orchard-power
state='/Library/Application Support/RunnerOrchard'
binary=/Library/PrivilegedHelperTools/io.runner-orchard-power
plist=/Library/LaunchDaemons/io.runner-orchard-power.plist
action=${1:-status}

if [[ "$action" == status ]]; then
  /bin/launchctl print "system/$label"
  /bin/cat "$state/status.json"
  exit
fi
if [[ $EUID != 0 ]]; then
  echo 'Run with sudo; this service changes system-wide sleep behavior.' >&2
  exit 1
fi
case "$action" in
  install)
    source_binary=${2:?Pass the compiled ApplePower binary}
    account=${3:?Pass the CI account name}
    uid=$(/usr/bin/id -u "$account")
    [[ $uid -ge 501 ]] || { echo 'Use a standard CI login account, never root.' >&2; exit 1; }
    [[ -f "$source_binary" && ! -L "$source_binary" ]] || exit 1
    # The first install refuses to take ownership of an existing sleep override.
    if [[ ! -f "$plist" ]] && /usr/sbin/ioreg -r -c IOPMrootDomain -d 1 | /usr/bin/grep -q '"SleepDisabled" = Yes'; then
      echo 'An existing sleep override is active. Restore it before installing.' >&2
      exit 1
    fi
    if /bin/launchctl print "system/$label" >/dev/null 2>&1; then
      /bin/launchctl bootout "system/$label"
    fi
    /usr/bin/pmset -a disablesleep 0
    /usr/bin/install -d -o root -g wheel -m 755 "$state" /Library/PrivilegedHelperTools
    /usr/bin/install -o root -g wheel -m 755 "$source_binary" "$binary"
    printf '%s\n' "$uid" > "$state/uid"
    /usr/sbin/chown root:wheel "$state/uid"
    /bin/chmod 644 "$state/uid"
    /usr/bin/touch "$state/enabled"
    /usr/sbin/chown root:wheel "$state/enabled"
    /bin/chmod 644 "$state/enabled"
    /bin/cat > "$plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.runner-orchard-power</string>
  <key>ProgramArguments</key><array><string>/Library/PrivilegedHelperTools/io.runner-orchard-power</string><string>--run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>2</integer>
  <key>ProcessType</key><string>Background</string>
  <key>ExitTimeOut</key><integer>20</integer>
  <key>StandardOutPath</key><string>/var/log/runner-orchard-power.log</string>
  <key>StandardErrorPath</key><string>/var/log/runner-orchard-power.log</string>
</dict></plist>
PLIST
    /usr/sbin/chown root:wheel "$plist"
    /bin/chmod 644 "$plist"
    /usr/bin/plutil -lint "$plist"
    /bin/launchctl bootstrap system "$plist"
    ;;
  pause)
    /bin/rm -f "$state/enabled"
    /usr/bin/pmset -a disablesleep 0
    ;;
  resume)
    [[ -f "$plist" && -f "$state/uid" ]] || { echo 'Install the service first.' >&2; exit 1; }
    /usr/bin/touch "$state/enabled"
    /usr/sbin/chown root:wheel "$state/enabled"
    /bin/chmod 644 "$state/enabled"
    ;;
  uninstall)
    /bin/rm -f "$state/enabled"
    if /bin/launchctl print "system/$label" >/dev/null 2>&1; then
      /bin/launchctl bootout "system/$label"
    fi
    /usr/bin/pmset -a disablesleep 0
    /bin/rm -f "$plist" "$binary" "$state/uid" "$state/status.json"
    /bin/rmdir "$state" 2>/dev/null || true
    ;;
  *) echo 'Usage: power-service.sh install BINARY ACCOUNT | status | pause | resume | uninstall' >&2; exit 2 ;;
esac
