import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { GuardrailsPage } from "./GuardrailsPage";
import { OrganizationsPage, TeamsPage } from "./IdentityAssociationPages";
import { RequestLogsPage } from "./RequestLogsPage";
import { RoutingPage } from "./RoutingPage";

function renderAuthenticated(node: React.ReactNode) {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  render(<AuthProvider>{node}</AuthProvider>);
}
const json = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });

describe("management pages", () => {
  it("simulates routing without invoking a provider", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input).includes("simulate") ? json({ candidates: ["primary"] }) : json({ strategy: "adaptive" }));
    renderAuthenticated(<RoutingPage />);
    await userEvent.type(screen.getByLabelText("Public model"), "gpt");
    await userEvent.type(screen.getByLabelText("Capabilities"), "chat, tools");
    await userEvent.click(screen.getByRole("button", { name: "Simulate" }));
    expect(await screen.findByText(/"primary"/)).toBeInTheDocument();
    expect(String(fetchMock.mock.calls.find((call) => String(call[0]).includes("simulate"))?.[1]?.body)).toContain('"capabilities":["chat","tools"]');
  });

  it("runs a metadata-only compliance check", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input).includes("compliance/check") ? json({ allowed: true, content_stored: false }) : json({ data: [] }));
    renderAuthenticated(<GuardrailsPage />);
    await userEvent.type(screen.getByLabelText("Policy"), "strict");
    await userEvent.type(screen.getByLabelText("Text projection"), "safe text");
    await userEvent.click(screen.getByRole("button", { name: "Run compliance check" }));
    expect(await screen.findByText(/"content_stored": false/)).toBeInTheDocument();
  });

  it("assigns a user to a team with roles", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [] }));
    renderAuthenticated(<TeamsPage />); await screen.findByText("No records found.");
    await userEvent.type(screen.getByLabelText("Team ID"), "team-a");
    await userEvent.type(screen.getByLabelText("User ID"), "user-a");
    await userEvent.type(screen.getByLabelText("Roles"), "operator, viewer");
    await userEvent.click(screen.getByRole("button", { name: "Assign" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/admin/v1/teams/team-a/members/user-a");
    expect(fetchMock.mock.calls[1][1]?.body).toBe('{"roles":["operator","viewer"]}');
  });

  it("assigns a team to an organization", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [] }));
    renderAuthenticated(<OrganizationsPage />); await screen.findByText("No records found.");
    await userEvent.type(screen.getByLabelText("Organization ID"), "org-a");
    await userEvent.type(screen.getByLabelText("Team ID"), "team-a");
    await userEvent.click(screen.getByRole("button", { name: "Assign" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/admin/v1/organizations/org-a/teams/team-a");
  });

  it("loads request-log privacy settings and details", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes("settings")) return json({ content_stored: false, retention_days: 730 });
      if (url.endsWith("/req-1")) return json({ request_id: "req-1", upstream_model: "gpt-versioned" });
      return json({ data: [{ request_id: "req-1", status: "ok", model: "gpt" }] });
    });
    renderAuthenticated(<RequestLogsPage />);
    expect(await screen.findByText("req-1")).toBeInTheDocument();
    expect(await screen.findByText(/content_stored/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Details" }));
    expect(await screen.findByText(/gpt-versioned/)).toBeInTheDocument();
  });
});
