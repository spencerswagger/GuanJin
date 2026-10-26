#!/usr/bin/env bash
set -uo pipefail

M2_DIR="${HOME}/.m2"
SETTINGS="${M2_DIR}/settings.xml"
PROXY_URL="${HTTPS_PROXY:-${https_proxy:-}}"

mkdir -p "${M2_DIR}"

if [ -n "${PROXY_URL}" ]; then
  proxy_host_port="${PROXY_URL#*://}"
  proxy_host_port="${proxy_host_port%%/*}"
  proxy_host="${proxy_host_port%%:*}"
  proxy_port="${proxy_host_port##*:}"
  [ "${proxy_port}" = "${proxy_host}" ] && proxy_port="80"
  cat > "${SETTINGS}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<settings>
  <mirrors>
    <mirror>
      <id>aliyun</id>
      <mirrorOf>central</mirrorOf>
      <url>https://maven.aliyun.com/repository/public</url>
    </mirror>
  </mirrors>
  <proxies>
    <proxy>
      <id>sandbox-https</id>
      <active>true</active>
      <protocol>https</protocol>
      <host>${proxy_host}</host>
      <port>${proxy_port}</port>
      <nonProxyHosts>localhost|127.0.0.1|*.svc|*.cluster.local</nonProxyHosts>
    </proxy>
    <proxy>
      <id>sandbox-http</id>
      <active>true</active>
      <protocol>http</protocol>
      <host>${proxy_host}</host>
      <port>${proxy_port}</port>
      <nonProxyHosts>localhost|127.0.0.1|*.svc|*.cluster.local</nonProxyHosts>
    </proxy>
  </proxies>
</settings>
EOF
fi

exec mvn -s "${SETTINGS}" "$@"
