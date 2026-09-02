import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { PlaygroundPage } from "./PlaygroundPage";

function authenticated() {
  sessionStorage.setItem("ai-gateway.admin-token", "playground-token");
  return render(<AuthProvider><PlaygroundPage /></AuthProvider>);
}

function streamResponse(chunks: string[]) {
  const encoder = new TextEncoder();
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    }
  }), { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

describe("PlaygroundPage", () => {
  it("discovers authorized models and renders incremental Chat Completions output", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/v1/models") return new Response(JSON.stringify({ data: [{ id: "gpt-z" }, { id: "gpt-a" }] }), { status: 200 });
      return streamResponse([
        'data: {"id":"chat-1","model":"gpt-a","choices":[{"delta":{"content":"Hel"}}]}\n\n',
        'data: {"id":"chat-1","model":"gpt-a","choices":[{"delta":{"content":"lo"}}],"usage":{"total_tokens":3}}\n\ndata: [DONE]\n\n'
      ]);
    });
    authenticated();
    expect(await screen.findByDisplayValue("gpt-a")).toBeInTheDocument();
    expect(screen.getByText("2 authorized models")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Instructions"), "Be concise");
    await userEvent.type(screen.getByLabelText("Message"), "hello");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("Hello")).toBeInTheDocument();
    expect(screen.getByText("chat-1")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();

    const request = fetchMock.mock.calls.find(([path]) => String(path) === "/v1/chat/completions")!;
    const body = JSON.parse(String(request[1]?.body));
    expect(body).toMatchObject({ model: "gpt-a", stream: true, max_completion_tokens: 256, messages: [{ role: "system", content: "Be concise" }, { role: "user", content: "hello" }] });
    expect(new Headers(request[1]?.headers).get("X-Session-ID")).toMatch(/^playground-/);

    await userEvent.type(screen.getByLabelText("Message"), "again");
    await userEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([path]) => String(path) === "/v1/chat/completions")).toHaveLength(2));
    const continuation = fetchMock.mock.calls.filter(([path]) => String(path) === "/v1/chat/completions")[1];
    expect(JSON.parse(String(continuation[1]?.body)).messages).toEqual([
      { role: "system", content: "Be concise" },
      { role: "user", content: "hello" },
      { role: "assistant", content: "Hello" },
      { role: "user", content: "again" }
    ]);
  });

  it("uses a JSON Responses fallback without replaying the request and preserves previous_response_id", async () => {
    let responseNumber = 0;
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/v1/models") return new Response(JSON.stringify({ data: [{ id: "response-model" }] }), { status: 200 });
      responseNumber++;
      return new Response(JSON.stringify({ id: `resp-${responseNumber}`, model: "response-model", output_text: responseNumber === 1 ? "First" : "Second", usage: { total_tokens: responseNumber + 1 } }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    authenticated();
    await screen.findByDisplayValue("response-model");
    await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Instructions"), "Use plain text");
    await userEvent.type(screen.getByLabelText("Message"), "first question");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("First")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Message"), "second question");
    await userEvent.click(screen.getByRole("button", { name: "Send message" }));
    expect(await screen.findByText("Second")).toBeInTheDocument();

    const requests = fetchMock.mock.calls.filter(([path]) => String(path) === "/v1/responses");
    expect(requests).toHaveLength(2);
    expect(JSON.parse(String(requests[0][1]?.body))).toMatchObject({ input: "first question", instructions: "Use plain text", stream: true, max_output_tokens: 256 });
    expect(JSON.parse(String(requests[1][1]?.body))).toMatchObject({ input: "second question", previous_response_id: "resp-1" });
    expect(new Headers(requests[0][1]?.headers).get("X-Session-ID")).toBe(new Headers(requests[1][1]?.headers).get("X-Session-ID"));
  });

  it("cancels an in-flight stream through AbortSignal", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      if (String(input) === "/v1/models") return new Response(JSON.stringify({ data: [{ id: "slow-model" }] }), { status: 200 });
      return new Promise<Response>((_resolve, reject) => {
        options?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      });
    });
    authenticated();
    await screen.findByDisplayValue("slow-model");
    await userEvent.type(screen.getByLabelText("Message"), "wait");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    await userEvent.click(await screen.findByRole("button", { name: "Stop" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Request cancelled");
    await waitFor(() => expect(screen.getByRole("button", { name: "Run request" })).toBeEnabled());
  });
});
