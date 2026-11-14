#!/bin/sh
# Removes the test instance completely. The real proxy and UI are not touched.
# Run as root:  sh deploy/test/teardown.sh
set -u
[ "$(id -u)" -eq 0 ] || { echo "Please run as root." >&2; exit 1; }

systemctl stop modbusui-test.service modbusui-test-apply.path modbusui-test-apply.service modbus-proxy-test.service 2>/dev/null
systemctl reset-failed modbus-proxy-test.service modbusui-test-apply.service 2>/dev/null
rm -f /etc/systemd/system/modbusui-test.service /etc/systemd/system/modbusui-test-apply.path \
      /etc/systemd/system/modbusui-test-apply.service /etc/systemd/system/modbus-proxy-test.service
systemctl daemon-reload
rm -rf /etc/modbus-proxy-test.yml /etc/modbusui-test /var/lib/modbusui-test /root/modbusui-test-pw.txt
if ss -tln | grep -qE ':(50291|50292|50293|8081) '; then
  echo "Warning: a test port is still listening." >&2
  exit 1
fi
echo "Test instance removed."
