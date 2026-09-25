#!/bin/sh
exec 3>>"$(dirname "$0")/stdin.log"
take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
take; printf '%s\n' '{"id":1,"jsonrpc":"2.0","result":{"agentCapabilities":{},"protocolVersion":1}}'
take; printf '%s\n' '{"id":2,"jsonrpc":"2.0","result":{"sessionId":"native-session"}}'
take
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"kind":"read","rawInput":{"path":"fixture.txt"},"sessionUpdate":"tool_call","status":"pending","title":"Read file","toolCallId":"native-tool"}}}'
printf '%s\n' '{"id":"permission-1","jsonrpc":"2.0","method":"session/request_permission","params":{"options":[{"kind":"allow_once","name":"Allow once","optionId":"allow"},{"kind":"reject_once","name":"Reject","optionId":"deny"}],"sessionId":"native-session","toolCall":{"kind":"read","rawInput":{"path":"fixture.txt"},"sessionUpdate":"tool_call","status":"pending","title":"Read file","toolCallId":"native-tool"}}}'
take
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"rawOutput":{"ok":true},"sessionUpdate":"tool_call_update","status":"completed","toolCallId":"native-tool"}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"content":{"text":"done","type":"text"},"sessionUpdate":"agent_message_chunk"}}}'
printf '%s\n' '{"id":3,"jsonrpc":"2.0","result":{"stopReason":"end_turn"}}'
while take; do :; done
