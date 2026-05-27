#!/bin/bash
# gotunnel client startup script
# Usage: ./start-client.sh -s <server_ip> [-p <server_port>] [-n <tunnel_name>] [-l <local_port>]

set -e

SERVER_IP="${GOTUNNEL_SERVER_IP:-}"
SERVER_PORT="${GOTUNNEL_SERVER_PORT:-6000}"
TUNNEL_NAME="${GOTUNNEL_TUNNEL_NAME:-default}"
LOCAL_PORT="${GOTUNNEL_LOCAL_PORT:-22}"
USE_TLS="${GOTUNNEL_USE_TLS:-false}"
DEBUG="${GOTUNNEL_DEBUG:-false}"
CLIENT_BIN="${GOTUNNEL_CLIENT_BIN:-./gotunnel-client}"

while getopts "s:p:n:l:tdh" opt; do
  case $opt in
    s) SERVER_IP="$OPTARG" ;;
    p) SERVER_PORT="$OPTARG" ;;
    n) TUNNEL_NAME="$OPTARG" ;;
    l) LOCAL_PORT="$OPTARG" ;;
    t) USE_TLS="true" ;;
    d) DEBUG="true" ;;
    h)
      echo "Usage: $0 -s <server_ip> [options]"
      echo "  -s  Server IP or hostname (required)"
      echo "  -p  Server control port (default: 6000)"
      echo "  -n  Tunnel name (default: default)"
      echo "  -l  Local port to forward (default: 22)"
      echo "  -t  Enable TLS"
      echo "  -d  Enable debug logging"
      exit 0
      ;;
    *) exit 1 ;;
  esac
done

if [ -z "$SERVER_IP" ]; then
  echo "Error: server IP is required. Use -s <server_ip>"
  exit 1
fi

CMD="$CLIENT_BIN -server ${SERVER_IP}:${SERVER_PORT} -local 127.0.0.1:${LOCAL_PORT} -tunnel ${TUNNEL_NAME}"
[ "$USE_TLS" = "true" ] && CMD="$CMD -tls"
[ "$DEBUG" = "true" ] && CMD="$CMD -debug"

echo "Starting gotunnel client:"
echo "  Server:  ${SERVER_IP}:${SERVER_PORT}"
echo "  Tunnel:  ${TUNNEL_NAME}"
echo "  Local:   127.0.0.1:${LOCAL_PORT}"
echo "  TLS:     ${USE_TLS}"
echo ""

exec $CMD
