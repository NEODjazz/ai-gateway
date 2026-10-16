import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { GlobalRateLimitsForm } from "./GlobalRateLimitsForm";

const defaults = { rpm: 60, tpm: 100000 };
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });

describe("GlobalRateLimitsForm", () => {
  it("shows effective values and persists overrides independently of routing plans", async () => {
    const calls: { method: string; body?: unknown }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const method = options?.method || "GET";
      calls.push({ method, body: options?.body ? JSON.parse(String(options.body)) : undefined });
      return response({ limits: method === "PUT" ? { rpm: 120, tpm: 200000 } : defaults, defaults, source: method === "PUT" ? "configuration" : "environment" });
    });
    render(<AuthProvider><GlobalRateLimitsForm /></AuthProvider>);
    const rpm = await screen.findByLabelText("Global requests per minute (RPM)");
    expect(rpm).toHaveValue(60);
    expect(screen.getByLabelText("Global tokens per minute (TPM)")).toHaveValue(100000);
    expect(screen.getByRole("button", { name: "Use Helm / ENV defaults" })).toBeDisabled();
    await userEvent.clear(rpm); await userEvent.type(rpm, "120");
    await userEvent.clear(screen.getByLabelText("Global tokens per minute (TPM)"));
    await userEvent.type(screen.getByLabelText("Global tokens per minute (TPM)"), "200000");
    await userEvent.click(screen.getByRole("button", { name: "Save global limits" }));
    expect(await screen.findByText("Global limits saved.")).toBeInTheDocument();
    expect(calls.find((call) => call.method === "PUT")?.body).toEqual({ rpm: 120, tpm: 200000 });
    await userEvent.click(screen.getByRole("button", { name: "Use Helm / ENV defaults" }));
    expect(await screen.findByText("Helm / ENV defaults restored.")).toBeInTheDocument();
    expect(rpm).toHaveValue(60);
    expect(calls.some((call) => call.method === "DELETE")).toBe(true);
  });

  it("preserves the draft on failed saves and never claims success", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => options?.method === "PUT" ? response({ error: { message: "Storage is unavailable" } }, 503) : response({ limits: defaults, defaults, source: "environment" }));
    render(<AuthProvider><GlobalRateLimitsForm /></AuthProvider>);
    const rpm = await screen.findByLabelText("Global requests per minute (RPM)");
    await userEvent.clear(rpm); await userEvent.type(rpm, "90");
    await userEvent.click(screen.getByRole("button", { name: "Save global limits" }));
    expect(await screen.findByText("Storage is unavailable")).toBeInTheDocument();
    expect(rpm).toHaveValue(90);
    expect(screen.queryByText("Global limits saved.")).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Save global limits" })).toBeEnabled());
  });
});
