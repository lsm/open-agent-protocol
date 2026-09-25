#!/bin/sh
exec 3>>"$(dirname "$0")/stdin.log"
take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
state() { printf '{"type":"response","id":"%s","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":%s,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":%s,"model":{"id":"model","provider":"fixture"}}}\n' "$1" "$2" "$3"; }
usage='"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}'
assistant="{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}],\"api\":\"messages\",\"provider\":\"fixture\",\"model\":\"model\",$usage,\"stopReason\":\"stop\",\"timestamp\":1}"
partial="{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hel\"}],\"api\":\"messages\",\"provider\":\"fixture\",\"model\":\"model\",$usage,\"stopReason\":\"stop\",\"timestamp\":1}"
aborted="{\"role\":\"assistant\",\"content\":[],\"api\":\"messages\",\"provider\":\"fixture\",\"model\":\"model\",$usage,\"stopReason\":\"aborted\",\"errorMessage\":\"Request was aborted\",\"timestamp\":2}"
user='{"role":"user","content":[{"type":"text","text":"hello"}],"timestamp":1}'

take; state req_1 0 false
take; state req_2 0 false
take; state req_3 0 false

take; printf '{"type":"response","id":"req_4","command":"prompt","success":true}\n'
printf '{"type":"agent_start"}\n'
printf '{"type":"turn_start"}\n'
printf '%s\n' "{\"type\":\"message_start\",\"message\":$user}"
printf '%s\n' "{\"type\":\"message_end\",\"message\":$user}"
printf '%s\n' "{\"type\":\"message_start\",\"message\":$partial}"
printf '%s\n' '{"type":"message_update","usage":{},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"consider"}}'
printf '%s\n' '{"type":"message_update","usage":{},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"hel"}}'
printf '%s\n' '{"type":"message_update","usage":{},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"lo"}}'
printf '%s\n' '{"type":"tool_execution_start","toolCallId":"a","toolName":"read","args":{"path":"a"}}'
printf '%s\n' '{"type":"tool_execution_update","toolCallId":"a","toolName":"read","args":{"path":"a"},"partialResult":{"text":"half"}}'
printf '%s\n' '{"type":"tool_execution_end","toolCallId":"a","toolName":"read","result":{"text":"a"},"isError":false}'
printf '%s\n' '{"type":"tool_execution_start","toolCallId":"b","toolName":"read","args":{"path":"b"}}'
printf '%s\n' '{"type":"tool_execution_end","toolCallId":"b","toolName":"read","result":{"message":"denied"},"isError":true}'
printf '%s\n' "{\"type\":\"message_end\",\"message\":$assistant}"
printf '%s\n' "{\"type\":\"turn_end\",\"message\":$assistant,\"toolResults\":[]}"
printf '%s\n' "{\"type\":\"agent_end\",\"messages\":[$assistant],\"willRetry\":false}"
printf '{"type":"agent_settled"}\n'

take; state req_5 2 false

take; printf '{"type":"response","id":"req_6","command":"prompt","success":true}\n'
printf '{"type":"agent_start"}\n'
printf '{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Proceed?","message":"Continue"}\n'
take; printf '{"type":"response","id":"req_7","command":"abort","success":true}\n'
printf '%s\n' "{\"type\":\"message_end\",\"message\":$aborted}"
printf '%s\n' "{\"type\":\"agent_end\",\"messages\":[$aborted],\"willRetry\":false}"
printf '{"type":"agent_settled"}\n'

take; state req_8 4 false

take; printf '{"type":"response","id":"req_9","command":"prompt","success":true}\n'
printf '{"type":"extension_ui_request","id":"ui-2","method":"select","title":"Pick","options":["left","right"]}\n'
printf '{"type":"agent_start"}\n'
take
printf '%s\n' "{\"type\":\"message_end\",\"message\":$assistant}"
printf '%s\n' "{\"type\":\"agent_end\",\"messages\":[$assistant],\"willRetry\":false}"
printf '{"type":"agent_settled"}\n'

while take; do :; done
