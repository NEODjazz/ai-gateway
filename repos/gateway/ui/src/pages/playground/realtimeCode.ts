import type { PlaygroundConnection } from "./requests";
import { realtimeProtocol, realtimeResponse, realtimeSession, realtimeSocketURL, type RealtimeSettings } from "./realtime";

export function realtimeCode(language: "javascript" | "python", connection: PlaygroundConnection, model: string, settings: RealtimeSettings): string {
  const origin = window.location.origin;
  const socket = realtimeSocketURL(connection, model, origin);
  const endpoint = new URL(connection.path("/v1/realtime/browser-tickets"), origin).toString();
  const session = JSON.stringify(realtimeSession(settings));
  const response = JSON.stringify(realtimeResponse(settings));
  const item = JSON.stringify({ type: "conversation.item.create", item: { type: "message", role: "user", content: [{ type: "input_text", text: "Your message" }] } });
  if (language === "javascript") return `// Run in the same-origin gateway browser console. The test key stays in memory.
const key = window.prompt("Gateway test API key");
if (!key) throw new Error("A test key is required");
const reply = await fetch(${JSON.stringify(endpoint)}, {
  method: "POST", credentials: "omit", cache: "no-store",
  headers: { Authorization: "Bearer " + key, "Content-Type": "application/json" },
  body: JSON.stringify({ model: ${JSON.stringify(model.trim())}, origin: window.location.origin, dialect: ${JSON.stringify(settings.dialect)} })
});
if (!reply.ok) throw new Error("Ticket request failed: " + reply.status);
const ticket = await reply.json();
const socket = new WebSocket(${JSON.stringify(socket)}, [${JSON.stringify(realtimeProtocol)}, "ai-gateway.realtime.ticket." + ticket.ticket]);
const timeout = setTimeout(() => socket.close(), 60000);
socket.onopen = () => socket.send(JSON.stringify(${session}));
let submitted = false;
socket.onmessage = ({ data }) => {
  const event = JSON.parse(data);
  if (event.type === "session.updated" && !submitted) {
    submitted = true;
    socket.send(JSON.stringify(${item}));
    socket.send(JSON.stringify(${response}));
  }
  if (["response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta"].includes(event.type)) console.log(event.delta);
  if (event.type === "error" || event.type === "response.done") { console.log(event.type, event.response?.status, event.response?.usage); socket.close(); }
};
socket.onerror = () => { console.error("Realtime connection failed"); socket.close(); };
socket.onclose = () => clearTimeout(timeout);`;
  return `# Requires the Python websockets package. Set GATEWAY_API_KEY in your environment.
import asyncio, json, os, urllib.request
from websockets.asyncio.client import connect

async def main():
    request = urllib.request.Request(${JSON.stringify(endpoint)},
        data=json.dumps({"model": ${JSON.stringify(model.trim())}, "origin": ${JSON.stringify(origin)}, "dialect": ${JSON.stringify(settings.dialect)}}).encode(),
        headers={"Authorization": "Bearer " + os.environ["GATEWAY_API_KEY"], "Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(request, timeout=30) as response:
        ticket = json.load(response)
    async with asyncio.timeout(60):
        async with connect(${JSON.stringify(socket)}, origin=${JSON.stringify(origin)},
                subprotocols=[${JSON.stringify(realtimeProtocol)}, "ai-gateway.realtime.ticket." + ticket["ticket"]], max_size=1048576) as socket:
            await socket.send(${JSON.stringify(session)})
            submitted = False
            async for data in socket:
                event = json.loads(data)
                if event["type"] == "session.updated" and not submitted:
                    submitted = True
                    await socket.send(${JSON.stringify(item)})
                    await socket.send(${JSON.stringify(response)})
                if event["type"] in ("response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta"):
                    print(event.get("delta", ""), end="", flush=True)
                if event["type"] in ("error", "response.done"):
                    print(event["type"], event.get("response", {}).get("status"), event.get("response", {}).get("usage"))
                    break

asyncio.run(main())`;
}
