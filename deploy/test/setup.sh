#!/bin/sh
# Sets up the test instance next to the real proxy (does NOT touch it):
#   test proxy  modbus-proxy-test.service  ports 50291/50292, targets in 192.0.2.0/24
#   test UI     modbusui-test.service      port 8081
#   handler     modbusui-test-apply.path/.service
# Requires: modbusui installed (install.sh), modbus-proxy-rs in /usr/local/bin, user modbusproxy.
# Run as root from anywhere:  sh deploy/test/setup.sh
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)

[ "$(id -u)" -eq 0 ] || { echo "Please run as root." >&2; exit 1; }
command -v modbusui >/dev/null || { echo "modbusui is not installed." >&2; exit 1; }
[ -x /usr/local/bin/modbus-proxy-rs ] || { echo "/usr/local/bin/modbus-proxy-rs not found." >&2; exit 1; }
for p in 50291 50292 8081; do
  if ss -tln | grep -q ":$p "; then echo "Port $p is already in use." >&2; exit 1; fi
done

install -m 644 "$HERE/modbus-proxy-test.yml" /etc/modbus-proxy-test.yml
install -d -o root -g root -m 755 /var/lib/modbusui-test /var/lib/modbusui-test/result /var/lib/modbusui-test/backup
install -d -o modbusui -g modbusui -m 700 /var/lib/modbusui-test/spool
(umask 077; python3 -I -c "import secrets;print(secrets.token_urlsafe(15))" > /root/modbusui-test-pw.txt)
(cat /root/modbusui-test-pw.txt; cat /root/modbusui-test-pw.txt) | modbusui -set-password -password-file /etc/modbusui-test/password
install -m 644 "$HERE"/modbus-proxy-test.service "$HERE"/modbusui-test.service \
  "$HERE"/modbusui-test-apply.path "$HERE"/modbusui-test-apply.service /etc/systemd/system/
systemctl daemon-reload
systemctl start modbus-proxy-test.service modbusui-test-apply.path modbusui-test.service
sleep 2
systemctl is-active modbus-proxy-test.service modbusui-test-apply.path modbusui-test.service
echo
echo "Test UI: http://<server-ip>:8081/  (password in /root/modbusui-test-pw.txt)"
echo "Run the automatic test:  python3 -I $HERE/ui-test.py"
echo "Remove everything again: sh $HERE/teardown.sh"
