import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { ModelCatalogPage } from "./ModelCatalogPage";

function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  render(<AuthProvider><ModelCatalogPage /></AuthProvider>);
}

describe("ModelCatalogPage", () => {
  it("renders canonical provider/model entries", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ version: "v1", models: [{ provider: "azure", model: "gpt", input_cost_per_1m: 1, currency: "USD" }] }), { status: 200 }));
    renderPage();
    expect(await screen.findByText("gpt")).toBeInTheDocument();
    expect(screen.getByText("azure")).toBeInTheDocument();
  });

  it("replaces an edited identity instead of duplicating it", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response(JSON.stringify({ version: "v1", models: [{ provider: "azure", model: "gpt", currency: "USD" }] }), { status: 200 })).mockResolvedValueOnce(new Response(JSON.stringify({ version: "v2", models: [{ provider: "azure", model: "gpt-new", currency: "USD" }] }), { status: 200 }));
    renderPage(); await screen.findByText("gpt");
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await userEvent.clear(screen.getByLabelText("Model")); await userEvent.type(screen.getByLabelText("Model"), "gpt-new");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const body = JSON.parse(String(fetchMock.mock.calls[1][1]?.body));
    expect(body.models).toHaveLength(1);
    expect(body.models[0].model).toBe("gpt-new");
  });

  it("deletes only catalog metadata", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response(JSON.stringify({ version: "v1", models: [{ provider: "azure", model: "gpt" }, { provider: "ollama", model: "phi3" }] }), { status: 200 })).mockResolvedValueOnce(new Response(JSON.stringify({ version: "v2", models: [{ provider: "ollama", model: "phi3" }] }), { status: 200 }));
    renderPage(); await screen.findByText("gpt");
    const deletes = screen.getAllByRole("button", { name: "Delete" });
    await userEvent.click(deletes[0]);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const body = JSON.parse(String(fetchMock.mock.calls[1][1]?.body));
    expect(body.models).toEqual([{ provider: "ollama", model: "phi3" }]);
  });
});
