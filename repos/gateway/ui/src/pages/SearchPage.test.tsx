import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { SearchPage } from "./SearchPage";

describe("SearchPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });
  it("sends bounded search controls and renders provider-validated results", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ object: "search", results: [{ title: "Guide", url: "https://docs.example.com/guide", snippet: "Production guidance" }] }), { status: 200, headers: { "Content-Type": "application/json" } }));
    render(<AuthProvider><SearchPage /></AuthProvider>);
    await userEvent.type(screen.getByLabelText("Search model"), "search-prod");
    await userEvent.type(screen.getByLabelText("Search query"), "gateway reliability");
    await userEvent.type(screen.getByLabelText("Search domains"), "docs.example.com, example.com");
    await userEvent.type(screen.getByLabelText("Search country"), "us");
    await userEvent.clear(screen.getByLabelText("Maximum search results")); await userEvent.type(screen.getByLabelText("Maximum search results"), "5");
    await userEvent.click(screen.getByRole("button", { name: "Run search" }));
    expect(await screen.findByRole("link", { name: "https://docs.example.com/guide" })).toHaveAttribute("href", "https://docs.example.com/guide");
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ model: "search-prod", query: "gateway reliability", max_results: 5, search_domain_filter: ["docs.example.com", "example.com"], country: "US" });
  });
});
