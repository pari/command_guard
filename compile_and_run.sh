#!/bin/bash
# Builds CommandGuard into ./instance/ and runs it from there.
# instance/ is a self-contained installation: binaries, a copy of ui/, email.tpl,
# config.json, pem_files/, logs/, mail_queue/ and run/. It holds your config,
# SSH keys and logs, so never delete it as if it were build output.
set -e
cd "$(dirname "$0")"
INSTANCE=instance
mkdir -p "$INSTANCE"
mkdir -p -m 700 "$INSTANCE"/pem_files "$INSTANCE"/logs "$INSTANCE"/mail_queue

echo "Compiling code"
go build -o "$INSTANCE/cguard" .
# The terminal daemon is NOT restarted here: open SSH sessions live inside it.
# cguard starts the new binary when needed. To load a new build now, stop this
# instance's daemon by its PID (this ends every open terminal session); do not
# use `pkill -f cg_sessiond`, which also matches other installations on the machine.
go build -o "$INSTANCE/cg_sessiond" ./sessiond
echo "Done Compiling"

echo "Refreshing ui/ in $INSTANCE"
rm -rf "$INSTANCE/ui" && cp -r ui "$INSTANCE/ui"
[ -f "$INSTANCE/email.tpl" ] || cp email.tpl "$INSTANCE/email.tpl"   # keep a customised template
if [ ! -f "$INSTANCE/config.json" ]; then
    cp config.json.sample "$INSTANCE/config.json" && chmod 600 "$INSTANCE/config.json"
    echo "Created $INSTANCE/config.json from config.json.sample: edit it before real use"
fi

echo "Stopping any running cguard"
pkill -x cguard || true

echo "Launching app from $INSTANCE/"
cd "$INSTANCE"
exec ./cguard "$@"
