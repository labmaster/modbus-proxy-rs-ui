#!/bin/sh
# Builds the release archives for all supported architectures (Linux, macOS, Git Bash).
#
#   sh build.sh            version from main.go
#   sh build.sh 1.2.0      explicit version
#
# Result in dist/:
#   modbusui-<version>-linux-amd64.tar.gz   (x86-64)
#   modbusui-<version>-linux-arm64.tar.gz   (ARM 64-bit, e.g. Raspberry Pi 3/4/5 with 64-bit OS)
#   modbusui-<version>-linux-armv7.tar.gz   (ARM 32-bit, e.g. Raspberry Pi 2/3/4 with 32-bit OS)
#   modbusui-linux-<arch>                   (plain binaries, used by install.sh in the source tree)
#   SHA256SUMS
#
# Needs Go 1.24+ only. The work is done by tools/release (pure Go), so the
# archives are identical whatever system they are built on.
set -eu
cd "$(dirname "$0")"
GO=${GO:-go}
command -v "$GO" >/dev/null 2>&1 || GO=/usr/local/go/bin/go
command -v "$GO" >/dev/null 2>&1 || { echo "Go not found (https://go.dev/dl/)" >&2; exit 1; }
GOPROXY=off exec "$GO" run ./tools/release "$@"
