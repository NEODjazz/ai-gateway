import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { ModelCatalogPage } from "./ModelCatalogPage";

function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  render(<AuthProvider><ModelCatalogPage /></AuthProvider>);
}

describe("ModelCatalogPage", () => {
  it("renders canonical provider/model entries", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ version: "v1", models: [{ provider: "azure", model: "gpt", input_cost_per_1m: 1, currency: "USD" }] }), { status: 200 }));
    renderPage();
    expect(await screen.findByText("gpt")).toBeInTheDocument();
    expect(screen.getByText("azure")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Search models"), "gpt");
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    const filters = await screen.findByRole("dialog", { name: "Filter models" });
    await userEvent.type(within(filters).getByLabelText("Provider"), "azure");
    await userEvent.type(within(filters).getByLabelText("Capability"), "chat");
    await userEvent.click(within(filters).getByRole("button", { name: "Apply filters" }));
    await userEvent.click(screen.getByRole("button", { name: /Model ↑/ }));
    await userEvent.click(screen.getByRole("button", { name: "Refresh table" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).includes("search=gpt") && String(path).includes("order=desc"))).toBe(true));
  });

  it("replaces an edited identity instead of duplicating it", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      if (String(input) === "/admin/v1/providers") return new Response(JSON.stringify({ data: [{ id: "azure", type: "openai-compatible" }] }), { status: 200 });
      if (options?.method === "PUT") return new Response(JSON.stringify({ version: "v2", models: [{ provider: "azure", model: "gpt-new", currency: "USD" }] }), { status: 200 });
      return new Response(JSON.stringify({ version: "v1", models: [{ provider: "azure", model: "gpt", capabilities: ["chat"], currency: "USD" }] }), { status: 200 });
    });
    renderPage(); await screen.findByText("gpt");
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure/gpt" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    expect(await screen.findByRole("option", { name: "azure — openai-compatible" })).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Model")); await userEvent.type(screen.getByLabelText("Model"), "gpt-new");
	await userEvent.type(screen.getByLabelText("Search cost / 1K"), "10");
	await userEvent.type(screen.getByLabelText("Character cost / 1M"), "15");
	await userEvent.type(screen.getByLabelText("Page cost / 1K"), "100");
	await userEvent.type(screen.getByLabelText("Audio cost / minute"), "0.12");
	await userEvent.type(screen.getByLabelText("Video cost / second"), "0.25");
    await userEvent.click(screen.getByLabelText("Capabilities"));
    await userEvent.click(screen.getByRole("option", { name: /Tools/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => call[1]?.method === "PUT")).toBe(true));
    const saveCall = fetchMock.mock.calls.find((call) => call[1]?.method === "PUT")!;
    const body = JSON.parse(String(saveCall[1]?.body));
    expect(body.models).toHaveLength(1);
    expect(body.models[0].model).toBe("gpt-new");
    expect(body.models[0].capabilities).toEqual(["chat", "tools"]);
	expect(body.models[0].search_cost_per_1k).toBe(10);
	expect(body.models[0].character_cost_per_1m).toBe(15);
	expect(body.models[0].page_cost_per_1k).toBe(100);
	expect(body.models[0].audio_cost_per_minute).toBe(0.12);
	expect(body.models[0].video_cost_per_second).toBe(0.25);
  });

  it("deletes only catalog metadata", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const source = { version: "v1", models: [{ provider: "azure", model: "gpt" }, { provider: "ollama", model: "phi3" }] };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_input, options) => options?.method === "PUT" ? new Response(JSON.stringify({ version: "v2", models: [{ provider: "ollama", model: "phi3" }] }), { status: 200 }) : new Response(JSON.stringify(source), { status: 200 }));
    renderPage(); await screen.findByText("gpt");
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure/gpt" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => call[1]?.method === "PUT")).toBe(true));
    const putCall = fetchMock.mock.calls.find((call) => call[1]?.method === "PUT")!;
    const body = JSON.parse(String(putCall[1]?.body));
    expect(body.models).toEqual([{ provider: "ollama", model: "phi3" }]);
  });

  it("previews a pricing diff and requires confirmation before applying", async () => {
    const current = { version: "v1", unknown_model_policy: "deny", models: [{ provider: "azure", model: "gpt", input_cost_per_1m: 1, currency: "USD" }, { provider: "ollama", model: "phi3" }] };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      if (options?.method === "PUT") return new Response(JSON.stringify({ ...current, version: "imported" }), { status: 200 });
      if (String(input).includes("?")) return new Response(JSON.stringify({ data: current.models, total: 2, version: "v1" }), { status: 200 });
      return new Response(JSON.stringify(current), { status: 200 });
    });
    Object.defineProperty(File.prototype, "text", { configurable: true, value: vi.fn().mockResolvedValue(JSON.stringify({ models: [{ provider: "azure", model: "gpt", input_cost_per_1m: 2, currency: "usd" }, { provider: "azure", model: "embed", input_cost_per_1m: 0.1, currency: "USD" }] })) });
    renderPage(); await screen.findByText("gpt");
    expect(screen.getByText("Import pricing").closest(".g-button")).not.toBeNull();
    await userEvent.upload(screen.getByLabelText("Pricing JSON file"), new File(["{}"], "pricing.json", { type: "application/json" }));
    expect(await screen.findByRole("dialog", { name: "Pricing import preview" })).toBeInTheDocument();
    expect(screen.getByText(/Source: pricing\.json/)).toBeInTheDocument();
    expect(screen.getByText("Added")).toBeInTheDocument(); expect(screen.getByText("Changed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply import" })).toBeDisabled();
    await userEvent.selectOptions(screen.getByLabelText("Import mode"), "replace");
    expect(screen.getByText("Removed")).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Confirm pricing import"));
    await userEvent.click(screen.getByRole("button", { name: "Apply import" }));
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => call[1]?.method === "PUT")).toBe(true));
    const body = JSON.parse(String(fetchMock.mock.calls.find((call) => call[1]?.method === "PUT")![1]?.body));
    expect(body.models).toHaveLength(2); expect(body.models[0].currency).toBe("USD"); expect(body.unknown_model_policy).toBe("deny");
  });
});
