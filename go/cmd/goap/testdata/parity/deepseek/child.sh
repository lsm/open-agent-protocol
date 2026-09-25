#!/bin/sh
exec 3>>"$(dirname "$0")/stdin.log"
take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
take; printf '{"id":1,"jsonrpc":"2.0","result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}\n'

take
printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"inserted":[{"content":[{"text":"hello","type":"text"}],"id":"m-1","role":"user","source":{"kind":"user"}}],"start":0,"target":"next-turn"},"seq":1,"time":1,"type":"agent/inbox/spliced"},"sessionId":"s1"}}'
printf '%s\n' '{"id":2,"jsonrpc":"2.0","result":{"messageId":"m-1"}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"s1","status":"running"}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"turn":1},"seq":2,"time":2,"type":"turn/start"},"sessionId":"s1"}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"step":1,"turn":1},"seq":3,"time":3,"type":"step/start"},"sessionId":"s1"}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"content":[{"text":"hello","type":"text"}],"id":"m-1","role":"user","source":{"kind":"user"}},"seq":4,"surfaceOp":"append","time":4,"type":"user/message"},"sessionId":"s1"}}'

printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"message":{"content":[{"text":"Hello","type":"text"}],"id":"a-6","role":"assistant","source":{"kind":"model","model":"deepseek-chat","provider":"fixture-provider"}},"stream":[{"type":"chunk","time":6,"chunk":{"type":"text-delta","index":0,"text":"Hel"}}],"step":1,"turn":1,"usage":{"inputTokens":1,"outputTokens":2}},"seq":6,"surfaceOp":"append","time":6,"type":"assistant/message"},"sessionId":"s1"}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"step":1,"turn":1},"seq":7,"time":7,"type":"step/end"},"sessionId":"s1"}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"reason":{"kind":"completed"},"turn":1},"seq":8,"time":8,"type":"turn/end"},"sessionId":"s1"}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"s1","status":"idle"}}'

while take; do
  id=$(printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\),"jsonrpc":"2.0","method":"shutdown".*/\1/p')
  if [ -n "$id" ]; then printf '{"id":%s,"jsonrpc":"2.0","result":{}}\n' "$id"; printf 'shutdown answered\n' >&3; exit 0; fi
done

