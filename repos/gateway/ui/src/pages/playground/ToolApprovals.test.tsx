import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToolApprovals } from "./ToolApprovals";
import { decideTool, editManualToolResult, provideManualToolResult, toolInvocations } from "./toolCalls";

const calls = () => toolInvocations("responses", { output: [{ type: "custom_tool_call", call_id: "custom_1", name: "query", input: '<script>alert("test")</script>' }] }, [], [{ type: "custom", name: "query" }]);

it.each(["custom", "function"])("reviews %s input as text and supports keyboard confirmation without submitting the conversation form", async (kind) => {
  const user = userEvent.setup(), submit = vi.fn(), execute = vi.fn(), continueConversation = vi.fn();
  function Review() {
    const [items, setItems] = useState(() => kind === "custom" ? calls() : toolInvocations("chat", { choices: [{ message: { tool_calls: [{ type: "function", id: "manual_1", function: { name: "query", arguments: '{"text":"<script>alert(1)</script>"}' } }] } }] }, [], [{ type: "function", function: { name: "query", parameters: { type: "object" } } }]));
    return <form onSubmit={(event) => { event.preventDefault(); submit(); }}><ToolApprovals calls={items} disabled={false} onExecute={execute} onContinue={continueConversation} onDecline={(index) => setItems((values) => values.map((item, i) => i === index ? decideTool(item, false) : item))} onResultEdit={(index, value) => setItems((values) => values.map((item, i) => i === index ? editManualToolResult(item, value) : item))} onUseResult={(index) => setItems((values) => values.map((item, i) => i === index ? provideManualToolResult(item, item.manualOutput ?? "") : item))} /></form>;
  }
  const view = render(<Review />);
  expect(view.container.querySelector("script")).toBeNull(); expect(screen.getByText(kind === "custom" ? '<script>alert("test")</script>' : '{"text":"<script>alert(1)</script>"}')).toBeInTheDocument();
  const input = screen.getByLabelText(kind === "custom" ? "Custom tool result custom_1" : "Function tool result manual_1"); await user.click(input); await user.keyboard('first{Enter}second'); expect(input).toHaveValue("first\nsecond");
  expect(submit).not.toHaveBeenCalled(); expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
  await user.tab(); expect(screen.getByRole("button", { name: "Use result for query" })).toHaveFocus(); await user.keyboard('{Enter}');
  expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeEnabled(); expect(submit).not.toHaveBeenCalled(); expect(execute).not.toHaveBeenCalled();
  await user.tab(); expect(screen.getByRole("button", { name: "Decline query" })).toHaveFocus(); await user.keyboard(' '); expect(screen.getByRole("region", { name: "Tool approvals" })).toHaveTextContent("query · declined");
  expect(continueConversation).not.toHaveBeenCalled(); await user.click(screen.getByRole("button", { name: "Continue with tool results" })); expect(continueConversation).toHaveBeenCalledOnce(); expect(submit).not.toHaveBeenCalled();
});

it("does not offer execution or confirmation when custom input is incomplete or no result handler exists", () => {
  const pending = { ...calls()[0], issue: "Tool input is incomplete. This call can only be declined." };
  render(<ToolApprovals calls={[pending]} disabled={false} onExecute={vi.fn()} onDecline={vi.fn()} onContinue={vi.fn()} />);
  expect(screen.getByLabelText("Custom tool result custom_1")).toBeDisabled(); expect(screen.getByRole("button", { name: "Use empty result for query" })).toBeDisabled(); expect(screen.getByRole("button", { name: "Decline query" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled(); expect(screen.queryByRole("button", { name: "Execute query" })).not.toBeInTheDocument();
});
