import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { CompliancePlayground } from "./CompliancePlayground";
import { playgroundConnection } from "./requests";

const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const setup = () => render(<CompliancePlayground connection={connection()} connectionChanged={false} connectionControls={null} />);

describe("Compliance Playground", () => {
  it("runs a quick check and reports a scanner failure visibly", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"error":{"message":"Scanner unavailable"}}', { status: 503 }));
    setup(); await userEvent.type(screen.getByLabelText("Guardrail policy"), "strict");
    await userEvent.type(screen.getByLabelText("Quick test prompt"), "Test prompt");
    await userEvent.click(screen.getByRole("button", { name: "Test policy" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Scanner unavailable");
    expect(screen.getByText("failed")).toBeInTheDocument();
    expect(mock.mock.calls[0][0]).toBe("/guardrails/apply_guardrail");
  });
  it("validates the policy before transport and displays actual totals while filtering", async () => {
    const mock = vi.spyOn(globalThis, "fetch"); setup();
    await userEvent.click(screen.getByRole("button", { name: "Run selected tests" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("valid guardrail policy"); expect(mock).not.toHaveBeenCalled();
    await userEvent.type(screen.getByLabelText("Search test cases"), "fictional");
    expect(screen.getByText("3 selected · 1 shown · 3 total")).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Select visible cases"));
    expect(screen.getByText("2 selected · 1 shown · 3 total")).toBeInTheDocument();
  });
  it("imports multiline CSV and creates a custom case through a keyboard-safe modal", async () => {
    setup();
    const file = new File(['category,framework,prompt,expected\r\nPrivacy,Custom,"hello,\nworld",block\r\n'], "test.csv", { type: "text/csv" });
    fireEvent.change(screen.getByLabelText("Import policy test CSV"), { target: { files: [file] } });
    expect(await screen.findByText("4 selected · 4 shown · 4 total")).toBeInTheDocument();
    expect(screen.getByText(/hello,/, { selector: "pre" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Add test case" }));
    await userEvent.type(screen.getByLabelText("Test prompt"), "Custom prompt");
    await userEvent.click(screen.getByRole("button", { name: "Add case" }));
    expect(screen.getByText("5 selected · 5 shown · 5 total")).toBeInTheDocument(); expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Add test case" })); await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(); expect(screen.getByRole("button", { name: "Add test case" })).toHaveFocus();
  });
  it("shows per-case outcomes including failed checks in batch totals", async () => {
    let index = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
      index++; return index === 3 ? new Response('{"error":{"message":"Offline"}}', { status: 503 }) : new Response(JSON.stringify({ allowed: index === 1, checks: {}, execution_id: `exec-${index}` }));
    });
    setup(); await userEvent.type(screen.getByLabelText("Guardrail policy"), "strict"); await userEvent.click(screen.getByRole("button", { name: "Run selected tests" }));
    expect(await screen.findByText("3/3 completed · 1 allowed · 1 blocked · 1 failed · 0 cancelled · 2 expected outcomes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export policy results" })).toBeEnabled();
  });
  it("reset aborts running checks and ignores transport responses that arrive late", async () => {
    const deferred: ((response: Response) => void)[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((resolve) => deferred.push(resolve)));
    setup(); await userEvent.type(screen.getByLabelText("Guardrail policy"), "strict"); await userEvent.click(screen.getByRole("button", { name: "Run selected tests" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(3));
    await userEvent.click(screen.getByRole("button", { name: "Reset results" }));
    expect(mock.mock.calls.every(([, options]) => options?.signal?.aborted)).toBe(true);
    await act(async () => { deferred.forEach((resolve) => resolve(new Response('{"allowed":true,"execution_id":"late-result"}'))); });
    expect(screen.queryByText(/late-result/)).not.toBeInTheDocument();
    expect(screen.getByText("0/0 completed · 0 allowed · 0 blocked · 0 failed · 0 cancelled · 0 expected outcomes")).toBeInTheDocument();
  });
});
