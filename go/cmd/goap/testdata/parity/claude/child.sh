#!/bin/sh
exec 3>>"$(dirname "$0")/stdin.log"
take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
field() { printf '%s' "$line" | sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }
take; printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}\n' "$(field request_id)"

take; uuid=$(field uuid)
printf '{"type":"system","subtype":"init","session_id":"native-1","tools":["Bash","Read","mcp__files__read"],"mcp_servers":[{"name":"files","status":"connected"}],"model":"claude-fake","uuid":"i1"}\n'
printf '{"type":"stream_event","event":{"type":"message_start"},"session_id":"native-1","parent_tool_use_id":null,"uuid":"e1","user_message_uuid":"%s"}\n' "$uuid"
printf '{"type":"assistant","message":{"id":"msg_1","model":"claude-fake","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"touch /tmp/x"}}]},"parent_tool_use_id":null,"session_id":"native-1","uuid":"a1"}\n'
printf '{"type":"control_request","request_id":"ask-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"touch /tmp/x"},"tool_use_id":"toolu_1"}}\n'
take; behavior=$(field behavior)
printf '{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"%s","is_error":false}]},"parent_tool_use_id":null,"session_id":"native-1","uuid":"u1"}\n' "$behavior"
printf '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}},"session_id":"native-1","parent_tool_use_id":null,"uuid":"e2"}\n'
printf '{"type":"result","subtype":"success","duration_ms":12,"is_error":false,"num_turns":1,"session_id":"native-1","stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":5},"terminal_reason":"completed","result":"answered %s","user_message_uuid":"%s","queued_turn_count":0,"uuid":"r1"}\n' "$behavior" "$uuid"
while take; do :; done
