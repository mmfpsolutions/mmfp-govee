#!/bin/sh
# Ensure config and logs directories exist and are writable by the app user
# This handles Docker volume mounts created by root on the host
for dir in /app/config /app/logs; do
    mkdir -p "$dir"
    chown app:app "$dir"
    chmod 755 "$dir"
done

# Drop to app user and exec the application
exec su-exec app "$@"
