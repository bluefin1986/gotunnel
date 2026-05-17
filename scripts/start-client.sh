#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="$ROOT_DIR/bin"
BIN="$BIN_DIR/gotunnel-client"

SERVER_ADDR="${GOTUNNEL_SERVER_ADDR:-127.0.0.1:6000}"
LOCAL_ADDR="${GOTUNNEL_LOCAL_ADDR:-127.0.0.1:5900}"
USE_TLS="${GOTUNNEL_TLS:-false}"
DEBUG="${GOTUNNEL_DEBUG:-false}"

usage() {
  cat <<USAGE
Usage:
  $(basename "$0") [options]

Options:
  -s, --server ADDR   gotunnel server control address, default: $SERVER_ADDR
  -l, --local ADDR    local service address to expose, default: $LOCAL_ADDR
      --tls           connect with TLS
      --debug         enable debug logs
      --build-only    only build client binary
  -h, --help          show help

Environment variables:
  GOTUNNEL_SERVER_ADDR=$SERVER_ADDR
  GOTUNNEL_LOCAL_ADDR=$LOCAL_ADDR
  GOTUNNEL_TLS=$USE_TLS
  GOTUNNEL_DEBUG=$DEBUG

Demos:
  # Expose local VNC 5900 through a server running on 1.2.3.4:6000
  $(basename "$0") --server 1.2.3.4:6000 --local 127.0.0.1:5900

  # Expose local SSH 22
  $(basename "$0") -s 1.2.3.4:6000 -l 127.0.0.1:22

  # Use environment variables
  GOTUNNEL_SERVER_ADDR=1.2.3.4:6000 GOTUNNEL_LOCAL_ADDR=127.0.0.1:8080 $(basename "$0")
USAGE
}

BUILD_ONLY=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    -s|--server)
      SERVER_ADDR="$2"; shift 2 ;;
    -l|--local)
      LOCAL_ADDR="$2"; shift 2 ;;
    --tls)
      USE_TLS=true; shift ;;
    --debug)
      DEBUG=true; shift ;;
    --build-only)
      BUILD_ONLY=true; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 1 ;;
  esac
done

mkdir -p "$BIN_DIR"
echo "[gotunnel] building client -> $BIN"
(cd "$ROOT_DIR" && go build -o "$BIN" ./client)

if [[ "$BUILD_ONLY" == "true" ]]; then
  echo "[gotunnel] build complete"
  exit 0
fi

echo "[gotunnel] starting client"
echo "  server: $SERVER_ADDR"
echo "  local : $LOCAL_ADDR"
echo "  tls   : $USE_TLS"
echo "  debug : $DEBUG"
exec "$BIN" -server "$SERVER_ADDR" -local "$LOCAL_ADDR" -tls="$USE_TLS" -debug="$DEBUG"
