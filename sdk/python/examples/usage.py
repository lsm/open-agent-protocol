"""Print terminal usage or typed protocol errors from all four public APIs.

Pass the stdio endpoint command followed by its arguments.
"""

import asyncio
from dataclasses import asdict
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
from oap_sdk import MakaiProtocolError, connect


async def main():
    observed = []
    async with connect(command=sys.argv[1], args=sys.argv[2:]) as client:
        for agent in (False, True):
            api = client.agent if agent else client.provider
            for stream in (False, True):
                result = {"agent": agent, "stream": stream}
                try:
                    request = {"model_ref": "fixture/other@usage",
                               "messages": [{"role": "user", "content": "usage"}]}
                    if stream:
                        terminal = []
                        tool_results = []
                        async for event in api.stream(**request):
                            if event.type == ("agent_end" if agent else "message_end"):
                                terminal.append(event)
                            if event.type == "tool_execution_end":
                                tool_results.append(event.result)
                        if tool_results:
                            result["tool_results"] = tool_results
                        assert len(terminal) == 1, terminal
                        usage = terminal[0].usage
                    else:
                        complete = api.run if agent else api.complete
                        usage = (await complete(**request)).usage
                    result["usage"] = None if usage is None else asdict(usage)
                except MakaiProtocolError as error:
                    result.update(error=error.code, error_type=type(error).__name__)
                observed.append(result)
    print(json.dumps(observed))


asyncio.run(main())
