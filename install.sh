#!/bin/sh
# modbusui installer – installs, updates or removes the web UI for modbus-proxy-rs.
#
#   sh install.sh                 install or update (safe to run again)
#   sh install.sh --port 8080     use another port for the web UI (default 80)
#   sh install.sh --uninstall     remove the UI (keeps config backups)
#   sh install.sh --purge         remove the UI including config backups
#
# Works from a release archive (contains a ready-made binary "modbusui") and
# from the source tree (uses dist/ from build.sh, or builds with Go if found).
# The proxy itself is never restarted by this script.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
BIN_DST=/usr/local/bin/modbusui
UNIT_DIR=/etc/systemd/system
STATE=/var/lib/modbusui
PORT=""
ACTION=install

say()  { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

while [ $# -gt 0 ]; do
  case "$1" in
    --port) [ $# -ge 2 ] || die "--port needs a value"; PORT=$2; shift 2 ;;
    --port=*) PORT=${1#--port=}; shift ;;
    --uninstall) ACTION=uninstall; shift ;;
    --purge) ACTION=purge; shift ;;
    -h|--help) usage ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "please run as root"
command -v systemctl >/dev/null 2>&1 || die "systemd (systemctl) is required"

# ---------------------------------------------------------------- uninstall
if [ "$ACTION" != install ]; then
  step "Removing modbusui"
  systemctl disable --now modbusui.service modbus-proxy-apply.path >/dev/null 2>&1 || true
  systemctl stop modbus-proxy-apply.service >/dev/null 2>&1 || true
  rm -f "$UNIT_DIR/modbusui.service" "$UNIT_DIR/modbus-proxy-apply.path" "$UNIT_DIR/modbus-proxy-apply.service"
  systemctl daemon-reload
  rm -f "$BIN_DST"
  rm -rf /etc/modbusui "$STATE/spool" "$STATE/result"
  if [ "$ACTION" = purge ]; then
    rm -rf "$STATE"
    say "Config backups removed as well."
  elif [ -d "$STATE/backup" ]; then
    say "Config backups kept in $STATE/backup (remove with --purge)."
  fi
  if id modbusui >/dev/null 2>&1; then userdel modbusui; fi
  say "modbusui removed. The proxy and /etc/modbus-proxy.yml are untouched."
  exit 0
fi

# ---------------------------------------------------------------- checks
step "Checking the system"
case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv8l) ARCH=armv7 ;;
  *) die "unsupported architecture $(uname -m) – build it yourself (see README)" ;;
esac
say "Architecture: $ARCH"

[ -f /etc/modbus-proxy.yml ] || say "WARNING: /etc/modbus-proxy.yml not found – the UI will show an error until it exists."
systemctl cat modbus-proxy.service >/dev/null 2>&1 || say "WARNING: modbus-proxy.service not found – start/stop will not work until it exists."
[ -S /run/dbus/system_bus_socket ] || say "WARNING: D-Bus system bus not found – the service status may not be shown."

# keep a custom port from an earlier installation unless --port is given
OLDPORT=""
if [ -f "$UNIT_DIR/modbusui.service" ]; then
  OLDPORT=$(sed -n 's/^ExecStart=.*-listen :\([0-9][0-9]*\).*/\1/p' "$UNIT_DIR/modbusui.service")
fi
PORT=${PORT:-${OLDPORT:-80}}
case "$PORT" in *[!0-9]*|"") die "invalid port: $PORT" ;; esac
[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die "invalid port: $PORT"
OURS=no
if [ "$PORT" = "$OLDPORT" ] && systemctl is-active --quiet modbusui.service; then OURS=yes; fi
if [ "$OURS" = no ] && command -v ss >/dev/null 2>&1 && ss -tlnH "sport = :$PORT" 2>/dev/null | grep -q .; then
  die "port $PORT is already in use by another program – choose another one with --port"
fi
say "Web UI port: $PORT"

# ---------------------------------------------------------------- binary
step "Finding the program"
TMPBIN=""
cleanup() { if [ -n "$TMPBIN" ]; then rm -f "$TMPBIN"; fi; }
trap cleanup EXIT

find_go() {
  for g in go /usr/local/go/bin/go; do
    if command -v "$g" >/dev/null 2>&1; then echo "$g"; return 0; fi
  done
  return 1
}

if [ -f "$HERE/go.mod" ] && GO=$(find_go); then
  # source tree with Go: always build from the current source (quick changes)
  say "Building from source with $($GO version) …"
  TMPBIN=$(mktemp)
  (cd "$HERE" && CGO_ENABLED=0 GOPROXY=off "$GO" build -trimpath -ldflags='-s -w' -o "$TMPBIN" .) \
    || die "build failed"
  SRC=$TMPBIN
elif [ -f "$HERE/modbusui" ]; then
  SRC="$HERE/modbusui"                          # release archive
elif [ -f "$HERE/dist/modbusui-linux-$ARCH" ]; then
  SRC="$HERE/dist/modbusui-linux-$ARCH"         # source tree after build.sh, no Go on this machine
else
  die "no binary for $ARCH found. Use the release archive for your architecture, run build.sh, or install Go (see README)."
fi
chmod 755 "$SRC"
VER=$("$SRC" -version 2>/dev/null) || die "$SRC cannot run on this system (wrong architecture?)"
say "Program: $VER"

# ---------------------------------------------------------------- install
step "Installing"
install -m 755 "$SRC" "$BIN_DST.new"
mv -f "$BIN_DST.new" "$BIN_DST"
say "$BIN_DST"

if ! id modbusui >/dev/null 2>&1; then
  useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin modbusui
  say "user modbusui created"
fi

install -d -o root -g root -m 755 "$STATE" "$STATE/result" "$STATE/backup"
install -d -o modbusui -g modbusui -m 700 "$STATE/spool"
say "$STATE/{spool,result,backup}"

[ -d "$HERE/deploy" ] || die "directory deploy/ with the systemd units is missing next to install.sh"
install -m 644 "$HERE/deploy/modbus-proxy-apply.path" "$HERE/deploy/modbus-proxy-apply.service" "$UNIT_DIR/"
sed "s/-listen :80\b/-listen :$PORT/" "$HERE/deploy/modbusui.service" > "$UNIT_DIR/modbusui.service.new"
chmod 644 "$UNIT_DIR/modbusui.service.new"
mv -f "$UNIT_DIR/modbusui.service.new" "$UNIT_DIR/modbusui.service"
systemctl daemon-reload
say "systemd units installed"

# ---------------------------------------------------------------- password
if [ ! -f /etc/modbusui/password ]; then
  step "Setting the login password"
  if [ -t 0 ]; then
    until "$BIN_DST" -set-password; do say "Please try again."; done
  else
    say "No terminal – set it later with: modbusui -set-password"
  fi
fi

# ---------------------------------------------------------------- start
step "Starting"
systemctl enable --now modbus-proxy-apply.path >/dev/null 2>&1
systemctl enable modbusui.service >/dev/null 2>&1
systemctl restart modbusui.service
sleep 1
systemctl is-active --quiet modbus-proxy-apply.path || die "modbus-proxy-apply.path is not active (systemctl status modbus-proxy-apply.path)"
systemctl is-active --quiet modbusui.service || die "modbusui.service is not active (see README, troubleshooting)"

ADDR=$(hostname -I 2>/dev/null | awk '{print $1}')
URL="http://${ADDR:-<server-ip>}"
[ "$PORT" = 80 ] || URL="$URL:$PORT"
say ""
say "modbusui is running: $URL/"
say "The proxy was not restarted."
