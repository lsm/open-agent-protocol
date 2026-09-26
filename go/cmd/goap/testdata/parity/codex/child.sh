#!/bin/sh
exec 3>>"$(dirname "$0")/stdin.log"
take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
id() { printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\),.*/\1/p'; }
take; printf '{"id":%s,"result":{"userAgent":"codex-fake","codexHome":"/codex","platformFamily":"unix","platformOs":"linux"}}\n' "$(id)"
take
take; printf '{"id":%s,"result":{"thread":{"id":"native-thread"}}}\n' "$(id)"

take; printf '{"id":%s,"result":{"turn":{"id":"turn-1","status":"inProgress"}}}\n' "$(id)"
printf '%s\n' '{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"turn-1","status":"inProgress"}}}'
printf '%s\n' '{"method":"item/agentMessage/delta","params":{"delta":"fixture-","itemId":"message-1","threadId":"native-thread","turnId":"turn-1"}}'
printf '%s\n' '{"method":"item/agentMessage/delta","params":{"delta":"ok","itemId":"message-1","threadId":"native-thread","turnId":"turn-1"}}'
printf '%s\n' '{"method":"item/completed","params":{"threadId":"native-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"message-1","text":"fixture-ok"}}}'
printf '%s\n' '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"turn-1","status":"completed"}}}'

take; printf '{"id":%s,"result":{"turn":{"id":"turn-2","status":"inProgress"}}}\n' "$(id)"
printf '%s\n' '{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"turn-2","status":"inProgress"}}}'
printf '%s\n' '{"method":"item/started","params":{"threadId":"native-thread","turnId":"turn-2","item":{"type":"commandExecution","id":"cmd-1","command":"true","status":"inProgress"}}}'
printf '%s\n' '{"method":"item/completed","params":{"threadId":"native-thread","turnId":"turn-2","item":{"type":"commandExecution","id":"cmd-1","command":"true","status":"completed","aggregatedOutput":"ok","exitCode":0}}}'
printf '%s\n' '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"turn-2","status":"completed"}}}'

take; printf '{"id":%s,"result":{"turn":{"id":"turn-3","status":"inProgress"}}}\n' "$(id)"
printf '%s\n' '{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"turn-3","status":"inProgress"}}}'
printf '%s\n' '{"method":"item/started","params":{"threadId":"native-thread","turnId":"turn-3","item":{"type":"commandExecution","id":"cmd-2","command":"rm -rf build","status":"inProgress"}}}'
printf '%s\n' '{"id":70,"method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"turn-3","itemId":"cmd-2","kind":"command","startedAtMs":10,"reason":"needs approval","availableDecisions":["accept","decline","cancel"]}}'
take
printf '%s\n' '{"method":"item/completed","params":{"threadId":"native-thread","turnId":"turn-3","item":{"type":"commandExecution","id":"cmd-2","command":"rm -rf build","status":"completed","aggregatedOutput":"removed","exitCode":0}}}'
printf '%s\n' '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"turn-3","status":"completed"}}}'

take; printf '{"id":%s,"result":{"turn":{"id":"turn-4","status":"inProgress"}}}\n' "$(id)"
printf '%s\n' '{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"turn-4","status":"inProgress"}}}'
take; printf '{"id":%s,"result":{}}\n' "$(id)"
printf '%s\n' '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"turn-4","status":"interrupted"}}}'

while take; do :; done
printf 'stdin closed\n' >&3
