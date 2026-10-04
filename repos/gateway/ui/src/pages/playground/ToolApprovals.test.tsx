import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToolApprovals } from "./ToolApprovals";
import { decideTool, editCustomToolResult, provideCustomToolResult, toolInvocations } from "./toolCalls";

const calls = () => toolInvocations("responses", { output: [{ type: "custom_tool_call", call_id: "custom_1", name: "query", input: '<script>alert("test")</script>' }] }, [], [{ type: "custom", name: "query" }]);

it("reviews custom input as text and supports keyboard confirmation without submitting the conversation form", async () => {
  const user = userEvent.setup(), submit = vi.fn(), execute = vi.fn(), continueConversation = vi.fn();
  function Review() {
    const [items, setItems] = useState(calls);
    return <form onSubmit={(event) => { event.preventDefault(); submit(); }}><ToolApprovals calls={items} disabled={false} onExecute={execute} onContinue={continueConversation} onDecline={(index) => setItems((values) => values.map((item, i) => i === index ? decideTool(item, false) : item))} onResultEdit={(index, value) => setItems((values) => values.map((item, i) => i === index ? editCustomToolResult(item, value) : item))} onUseResult={(index) => setItems((values) => values.map((item, i) => i === index ? provideCustomToolResult(item, item.manualOutput ?? "") : item))} /></form>;
  }
  const view = render(<Review />);
  expect(view.container.querySelector("script")).toBeNull(); expect(screen.getByText('<script>alert("test")</script>')).toBeInTheDocument();
  const input = screen.getByLabelText("Custom tool result custom_1"); await user.click(input); await user.keyboard('first{Enter}second'); expect(input).toHaveValue("first\nsecond");
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
