#!/bin/sh
exec 3>>"$(dirname "$0")/stdin.log"
take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
printf '{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"e3b0c44298fc1c149afbf4c8996fb924"}}}\n'
take; printf '{"id":1,"jsonrpc":"2.0","result":{"session_id":"sess0001","stored_session_id":"key0001","message_count":0,"info":{}}}\n'

take; printf '{"id":2,"jsonrpc":"2.0","result":{"status":"streaming"}}\n'
printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"sess0001","seq":1}}\n'

printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.delta","session_id":"sess0001","seq":2,"payload":{"text":"fixture-ok"}}}\n'
printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.complete","session_id":"sess0001","seq":3,"payload":{"text":"fixture-ok","status":"complete","usage":{"input":1,"output":2,"total":3}}}}\n'

while take; do :; done
