import asyncio
from dataclasses import asdict
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
from oap_sdk import connect


async def main():
    other = sys.argv[2] == "--non-codex"
    arguments = (["sdk/typescript/test/fixtures/oap-server.js"] if other else
                 ["serve", "agent", "--backend", "codex", "--config", sys.argv[2]])
    model = "fixture/openai-responses@bash-no-exit" if other else "fixture/openai-responses@glm-test"
    async with connect(command=sys.argv[1], args=arguments) as client:
        events = [asdict(event) async for event in client.agent.stream(
            model_ref=model, messages=[{"role": "user", "content": "read the code"}])
            if event.type == "tool_execution_end"]
    print(json.dumps(events))
    if other:
        assert len(events) == 2
        assert all(event["is_error"] is False for event in events)
        assert events[1]["result"] is None
        return
    assert len(events) == 8
    for i, event in enumerate(events):
        assert event["is_error"] is (i not in (0, 5)), (i, event)
        if i < 6:
            assert event["result"]["exitCode"] == [0, 1, -7, None, None, 0][i], (i, event)
            assert event["result"]["command"] == "git diff HEAD~1"
            assert event["result"]["cwd"] == "/workspace/repo"


asyncio.run(main())
