#!/bin/sh
# Provision the Debian (proot) runtime used for glibc-native developer tools:
# Node.js (official linux-arm64 build), uv for Python projects, git and a C
# toolchain for packages that compile during installation. Idempotent; run
# inside the container (proot-distro login debian -- sh setup-debian.sh).
set -eu
NODE_MAJOR="${NODE_MAJOR:-22}"
export DEBIAN_FRONTEND=noninteractive
if [ ! -f /var/lib/tidalbridge-apt-done ]; then
  apt-get update -q
  apt-get install -y -q --no-install-recommends ca-certificates curl xz-utils git procps python3 python3-venv python3-pip build-essential pkg-config
  touch /var/lib/tidalbridge-apt-done
fi
if [ ! -x /opt/node/bin/node ] || ! /opt/node/bin/node --version | grep -q "^v${NODE_MAJOR}\."; then
  version=$(curl -fsSL https://nodejs.org/dist/index.json | python3 -c "import json,sys; print(next(r['version'] for r in json.load(sys.stdin) if r['version'].startswith('v${NODE_MAJOR}.')))")
  archive="node-${version}-linux-arm64.tar.xz"
  curl -fsSL "https://nodejs.org/dist/${version}/${archive}" -o "/tmp/${archive}"
  curl -fsSL "https://nodejs.org/dist/${version}/SHASUMS256.txt" | grep " ${archive}\$" > /tmp/node.sha256
  (cd /tmp && sha256sum -c node.sha256)
  rm -rf /opt/node.new && mkdir -p /opt/node.new
  tar -xJf "/tmp/${archive}" -C /opt/node.new --strip-components=1
  rm -rf /opt/node && mv /opt/node.new /opt/node
  rm -f "/tmp/${archive}" /tmp/node.sha256
fi
if [ ! -x /root/.local/bin/uv ]; then
  curl -LsSf https://astral.sh/uv/install.sh | env UV_NO_MODIFY_PATH=1 sh
fi
echo "node $(/opt/node/bin/node --version) npm $(/opt/node/bin/npm --version) uv $(/root/.local/bin/uv --version | cut -d' ' -f2) python $(python3 --version | cut -d' ' -f2) git $(git --version | cut -d' ' -f3)"
echo TIDALBRIDGE_DEBIAN_READY
