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
    expect(screen.getByText("strict: failed")).toBeInTheDocument();
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


describe("Multiple policy Compliance UI", () => {
  const catalog = (policies: string[]) => JSON.stringify({ mcp_servers: [], mcp_toolsets: [], agents: [], tags: [], policies, truncated: false });
  it("loads authorized choices and shows all per-policy outcomes without hiding a failure", async () => {
    const transport = vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      if (String(path).includes("/catalog")) return new Response(catalog(["privacy", "strict"]));
      const body = JSON.parse(String(options?.body));
      return body.guardrail_name === "privacy" ? new Response('{"allowed":false,"execution_id":"privacy-check"}') : new Response('{"error":{"message":"Scanner offline"}}', { status: 503 });
    });
    setup(); await userEvent.click(screen.getByRole("button", { name: "Load policies" }));
    await userEvent.click(await screen.findByLabelText("Test policy privacy")); await userEvent.click(screen.getByLabelText("Test policy strict"));
    expect(screen.getByLabelText("Guardrail policy")).toHaveValue("privacy, strict");
    await userEvent.click(screen.getByRole("button", { name: "Run selected tests" }));
    expect(await screen.findByText("6/6 completed · 0 allowed · 3 blocked · 3 failed · 0 cancelled · 2 expected outcomes")).toBeInTheDocument();
    expect(screen.getAllByText("privacy: blocked")).toHaveLength(3); expect(screen.getAllByText("strict: failed")).toHaveLength(3);
    expect(screen.getByText("3 prompts × 2 policies · totals count individual policy checks.")).toBeInTheDocument();
    expect(transport).toHaveBeenCalledTimes(7);
    // A subsequent quick check must not change the previous batch's target snapshot.
    await userEvent.click(screen.getByRole("tab", { name: "Quick test" }));
    await userEvent.clear(screen.getByLabelText("Guardrail policy")); await userEvent.type(screen.getByLabelText("Guardrail policy"), "privacy");
    await userEvent.type(screen.getByLabelText("Quick test prompt"), "quick"); await userEvent.click(screen.getByRole("button", { name: "Test policy" }));
    expect(await screen.findByText("privacy: blocked")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Batch results" }));
    expect(screen.getByText("3 prompts × 2 policies · totals count individual policy checks.")).toBeInTheDocument();
    expect(screen.getAllByText("strict: failed")).toHaveLength(3);
    const create = vi.fn((_blob: Blob) => "blob:compliance"), revoke = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revoke });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    try {
      await userEvent.click(screen.getByRole("button", { name: "Export policy results" }));
      const csv = await new Promise<string>((resolve) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.readAsText(create.mock.calls[0][0]); });
      expect(csv.split("\r\n")).toHaveLength(8);
      expect(csv.match(/"privacy"/g)).toHaveLength(3);
      expect(csv.match(/"strict"/g)).toHaveLength(3);
      expect(csv.match(/"Scanner offline"/g)).toHaveLength(3);
      expect(csv.match(/"privacy-check"/g)).toHaveLength(3);
      expect(revoke).toHaveBeenCalledWith("blob:compliance");
    } finally { Reflect.deleteProperty(URL, "createObjectURL"); Reflect.deleteProperty(URL, "revokeObjectURL"); }
  });
  it("limits catalog selection to four distinct targets and permits deselection", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(catalog(["a", "b", "c", "d", "e"])));
    setup(); await userEvent.click(screen.getByRole("button", { name: "Load policies" }));
    for (const name of ["a", "b", "c", "d", "e"]) await userEvent.click(await screen.findByLabelText(`Test policy ${name}`));
    expect(screen.getByLabelText("Guardrail policy")).toHaveValue("a, b, c, d");
    expect(screen.getByRole("alert")).toHaveTextContent("four distinct");
    expect(screen.getByLabelText("Test policy e")).not.toBeChecked();
    await userEvent.click(screen.getByLabelText("Test policy a")); await userEvent.click(screen.getByLabelText("Test policy e"));
    expect(screen.getByLabelText("Guardrail policy")).toHaveValue("b, c, d, e");
  });
  it("discards catalog responses and selected names when the credential changes", async () => {
    let resolve!: (response: Response) => void;
    const transport = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((done) => { resolve = done; }));
    const initial = connection();
    const { rerender } = render(<CompliancePlayground connection={initial} connectionChanged={false} connectionControls={null} />);
    await userEvent.type(screen.getByLabelText("Guardrail policy"), "private");
    await userEvent.click(screen.getByRole("button", { name: "Load policies" }));
    rerender(<CompliancePlayground connection={connection()} connectionChanged={false} connectionControls={null} />);
    expect(transport.mock.calls[0][1]?.signal?.aborted).toBe(true);
    await act(async () => resolve(new Response(catalog(["private"]))));
    expect(screen.queryByLabelText("Test policy private")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Guardrail policy")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Load policies" })).toBeEnabled();
  });
  it("clears catalog choices and results when the optional authorization model changes", async () => {
    const transport = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(catalog(["private"])));
    setup(); await userEvent.click(screen.getByRole("button", { name: "Load policies" }));
    await userEvent.click(await screen.findByLabelText("Test policy private"));
    await userEvent.type(screen.getByLabelText("Policy test model"), "model-a");
    expect(screen.queryByLabelText("Test policy private")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Guardrail policy")).toHaveValue("");
    await userEvent.click(screen.getByRole("button", { name: "Load policies" }));
    await screen.findByLabelText("Test policy private");
    expect(transport.mock.calls[1][0]).toBe("/v1/playground/catalog?model=model-a");
  });
  it("reports partial catalog failures while retaining returned authorized choices", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ...JSON.parse(catalog(["strict"])), policy_error: "Partial discovery unavailable", truncated: true })));
    setup(); await userEvent.click(screen.getByRole("button", { name: "Load policies" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Partial discovery unavailable");
    expect(screen.getByRole("status")).toHaveTextContent("256");
    expect(screen.getByLabelText("Test policy strict")).toBeInTheDocument();
  });
});
