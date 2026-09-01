import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { LoggingPage } from "./LoggingPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const destination = { id: "security", name: "Security events", type: "webhook", url: "https://logs.example.test/events", event_types: ["request_outcome"], enabled: true, secret_configured: true };
const list = { data: [destination], delivery: { queued: 12, delivered: 10, failed: 1, dropped: 1 }, content_stored: false };

function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter><AuthProvider><LoggingPage /></AuthProvider></MemoryRouter>);
}

describe("LoggingPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("shows delivery health and the metadata-only content boundary", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      return json(list);
    });
    renderPage();
    expect(await screen.findByText("Security events")).toBeInTheDocument();
    expect(within(screen.getByText("Queued").closest("article")!).getByText("12")).toBeInTheDocument();
    expect(within(screen.getByText("Delivered").closest("article")!).getByText("10")).toBeInTheDocument();
    expect(within(screen.getByText("Failed").closest("article")!).getByText("1")).toBeInTheDocument();
    expect(within(screen.getByText("Dropped").closest("article")!).getByText("1")).toBeInTheDocument();
    expect(screen.getByText(/Prompts, responses, raw errors/)).toBeInTheDocument();
    expect(screen.getByText("Configured")).toBeInTheDocument();
  });

  it("creates and edits a destination without reading or overwriting its secret", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/logging/destinations" && !options?.method) return json(list);
      if (url.includes("/admin/v1/logging/destinations/") && options?.method === "PUT") return json(destination);
      return json({ data: [] });
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Create Logging Destination" }));
    const create = screen.getByRole("dialog", { name: "Create logging destination" });
    await userEvent.type(within(create).getByLabelText("Logging destination ID"), "siem");
    await userEvent.type(within(create).getByLabelText("Logging destination name"), "SIEM");
    await userEvent.type(within(create).getByLabelText("Logging destination URL"), "https://siem.example.test/events");
    await userEvent.type(within(create).getByLabelText("Logging destination secret"), "write-only-test-secret");
    await userEvent.click(within(create).getByRole("button", { name: "Create destination" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/logging/destinations/siem" && options?.method === "PUT")).toBe(true));
    const saved = fetchMock.mock.calls.find(([url, options]) => url === "/admin/v1/logging/destinations/siem" && options?.method === "PUT");
    expect(JSON.parse(String(saved?.[1]?.body))).toMatchObject({ name: "SIEM", secret: "write-only-test-secret", event_types: ["request_outcome"], enabled: true });

    await userEvent.click(screen.getByRole("button", { name: "Actions for logging destination security" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    const edit = screen.getByRole("dialog", { name: "Edit logging destination" });
    expect(within(edit).getByText(/cannot be read back/)).toBeInTheDocument();
    await userEvent.clear(within(edit).getByLabelText("Logging destination name"));
    await userEvent.type(within(edit).getByLabelText("Logging destination name"), "Security updated");
    await userEvent.click(within(edit).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/logging/destinations/security" && options?.method === "PUT")).toBe(true));
    const updated = fetchMock.mock.calls.find(([url, options]) => url === "/admin/v1/logging/destinations/security" && options?.method === "PUT");
    expect(JSON.parse(String(updated?.[1]?.body))).not.toHaveProperty("secret");
  });

  it("tests and deletes callbacks from the actions menu", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/logging/destinations" && !options?.method) return json(list);
      if (url.endsWith("/security/test") && options?.method === "POST") return json({ status: "available", latency_ms: 24, content_sent: false });
      if (url.endsWith("/security") && options?.method === "DELETE") return new Response(null, { status: 204 });
      return json({ data: [] });
    });
    renderPage();
    await screen.findByText("Security events");
    await userEvent.click(screen.getByRole("button", { name: "Actions for logging destination security" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Test" }));
    expect(await screen.findByText("security is available")).toBeInTheDocument();
    expect(screen.getByText(/24 ms. Content sent: no/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for logging destination security" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/logging/destinations/security" && options?.method === "DELETE")).toBe(true));
  });
});
