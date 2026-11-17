#!/bin/sh
set -eu

case "${GODB_NODE_ID:-}" in
    1|2|3) ;;
    *) echo 'GODB_NODE_ID must be 1, 2, or 3' >&2; exit 1 ;;
esac

if [ ! -e /var/lib/godb/member.db ]; then
    if [ -n "$(ls -A /var/lib/godb)" ]; then
        echo 'Database is missing from a nonempty data directory; refusing to initialize it' >&2
        exit 1
    fi
    godb database create /var/lib/godb/member.db
fi

exec godb server \
    --database /var/lib/godb/member.db \
    --group 11111111-1111-4111-8111-111111111111 \
    --node "$GODB_NODE_ID" \
    --peer 1=node1:7000 --peer 2=node2:7000 --peer 3=node3:7000 \
    --listen 0.0.0.0:7000 --bootstrap \
    --tls-cert /etc/godb/tls/member.crt \
    --tls-key /etc/godb/tls/member.key \
    --tls-ca /etc/godb/tls/ca.crt \
    --metrics-listen 0.0.0.0:9091 \
    --otlp-http-endpoint "${GODB_OTLP_HTTP_ENDPOINT:-}" \
    --trace-sample-ratio "${GODB_TRACE_SAMPLE_RATIO:-0.1}"
