#!/bin/sh
# CommandGuard resolves ui/, logs/, pem_files/, run/ and mail_queue/ relative to
# the working directory, which is the /app/data volume inside the container.
set -e
cd /app/data
[ -e ui ] || ln -s /app/ui ui
[ -e email.tpl ] || cp /app/email.tpl email.tpl
mkdir -p pem_files logs mail_queue
if [ ! -f config.json ]; then
    echo "No config.json in the data volume: starting from config.json.sample (bind_address set to 0.0.0.0)."
    sed 's/"bind_address": "127.0.0.1"/"bind_address": "0.0.0.0"/' /app/config.json.sample > config.json
    chmod 600 config.json
fi
exec /app/cguard "$@"
