import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { PoliciesPage } from "./PoliciesPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const policies = [
  { name: "strict", description: "DLP and antivirus", dlp: true, av: true, enabled: true },
  { name: "disabled", description: "Disabled fixture", dlp: true, av: false, enabled: false },
];

function renderPage(entry = "/policies") {
  sessionStorage.setItem("ai-gateway.admin-token", "policy-token");
  return render(<MemoryRouter initialEntries={[entry]}><AuthProvider><PoliciesPage /></AuthProvider></MemoryRouter>);
}

function directoryResponse(url: string) {
  if (url === "/admin/v1/guardrail-policies") return json({ data: policies });
  if (url === "/admin/v1/teams?limit=500") return json({ data: [{ id: "care-a", name: "Care A" }] });
  if (url.startsWith("/admin/v1/keys?")) return json({ data: [{ id: "vk-1", alias: "clinical-prod" }] });
  if (url === "/admin/v1/model-catalog") return json({ models: [{ model: "gpt-5.6", provider: "azure" }] });
  if (url === "/admin/v1/tags") return json({ data: [{ name: "hipaa", description: "Regulated", enabled: true }] });
  return undefined;
}

describe("PoliciesPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("joins policy health and creates a wildcard-aware scoped attachment", async () => {
    let attachments = [
      { id: "global-strict", policy_name: "strict", scope: "*" as const },
      { id: "stale", policy_name: "missing-policy", scope: "specific" as const, models: ["legacy-*"] },
    ];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const directory = directoryResponse(url);
      if (directory) return directory;
      if (url === "/admin/v1/policy-attachments" && (!init?.method || init.method === "GET")) return json({ data: attachments });
      if (url === "/admin/v1/policy-attachments/clinical-production" && init?.method === "PUT") {
        attachments = [...attachments, { id: "clinical-production", ...JSON.parse(String(init.body)) }];
        return json(attachments[2]);
      }
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${url}` } }, 500);
    });
    renderPage();
    expect(await screen.findByText("global-strict")).toBeInTheDocument();
    expect(screen.getByText("Fail-closed risks").closest("article")).toHaveTextContent("1");
    await userEvent.click(screen.getByRole("button", { name: "Create Policy Attachment" }));
    const dialog = screen.getByRole("dialog", { name: "Create Policy Attachment" });
    await userEvent.type(within(dialog).getByLabelText("Attachment ID"), "clinical:production");
    await userEvent.selectOptions(within(dialog).getByLabelText("Guardrail policy"), "strict");
    await userEvent.type(within(dialog).getByRole("combobox", { name: "Teams" }), "care-*{enter}");
    await userEvent.type(within(dialog).getByRole("combobox", { name: "Models" }), "gpt-*{enter}");
    await userEvent.type(within(dialog).getByRole("combobox", { name: "Tags" }), "hipaa{enter}");
    expect(within(dialog).getByLabelText("Scope impact preview")).toHaveTextContent("every configured dimension");
    expect(within(dialog).getByRole("button", { name: "Remove team care-*" })).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Save attachment" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("dot, underscore and hyphen");
    expect(fetchMock.mock.calls.some(([url, init]) => String(url).includes("clinical%3Aproduction") && init?.method === "PUT")).toBe(false);
    await userEvent.clear(within(dialog).getByLabelText("Attachment ID"));
    await userEvent.type(within(dialog).getByLabelText("Attachment ID"), "clinical-production");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save attachment" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url) === "/admin/v1/policy-attachments/clinical-production" && init?.method === "PUT")).toBe(true));
    const save = fetchMock.mock.calls.find(([url, init]) => String(url) === "/admin/v1/policy-attachments/clinical-production" && init?.method === "PUT")!;
    expect(JSON.parse(String(save[1]?.body))).toEqual({ policy_name: "strict", scope: "specific", teams: ["care-*"], keys: [], models: ["gpt-*"], tags: ["hipaa"] });
    expect(await screen.findByText("clinical-production")).toBeInTheDocument();
  });

  it("simulates the exact runtime matcher and exposes fail-closed issues", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const directory = directoryResponse(url);
      if (directory) return directory;
      if (url === "/admin/v1/policy-attachments") return json({ data: [] });
      if (url === "/admin/v1/policy-attachments/resolve" && init?.method === "POST") return json({
        matched_attachments: [
          { id: "global-strict", policy_name: "strict", scope: "*", matched_via: ["global"], policy_status: "enabled", dlp: true, av: true },
          { id: "stale", policy_name: "missing-policy", scope: "specific", matched_via: ["team", "model"], policy_status: "missing", dlp: false, av: false },
        ],
        effective_policies: ["strict"], dlp: true, av: true, enforceable: false,
        issues: [{ attachment_id: "stale", policy_name: "missing-policy", code: "policy_missing" }],
      });
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${url}` } }, 500);
    });
    renderPage("/policies?view=simulator&team_id=care-a&credential_alias=clinical-prod&model=gpt-5.6&tag=hipaa");
    await screen.findByText("Runtime policy simulator");
    expect(screen.getByLabelText("Team ID")).toHaveValue("care-a");
    expect(screen.getByLabelText("Virtual key alias")).toHaveValue("clinical-prod");
    expect(screen.getByLabelText("Model")).toHaveValue("gpt-5.6");
    expect(screen.getByRole("button", { name: "Remove tag hipaa" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Simulate" }));
    const result = await screen.findByLabelText("Policy resolution result");
    expect(result).toHaveTextContent("Fail closed");
    expect(result).toHaveTextContent("policy_missing");
    expect(result).toHaveTextContent("global");
    expect(result).toHaveTextContent("team");
    const call = fetchMock.mock.calls.find(([url, init]) => String(url) === "/admin/v1/policy-attachments/resolve" && init?.method === "POST")!;
    expect(JSON.parse(String(call[1]?.body))).toEqual({ team_id: "care-a", credential_alias: "clinical-prod", model: "gpt-5.6", tags: ["hipaa"] });
  });

  it("opens a create form prefilled from an identity workspace", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      const directory = directoryResponse(url);
      if (directory) return directory;
      if (url === "/admin/v1/policy-attachments") return json({ data: [] });
      return json({ error: { message: `unexpected GET ${url}` } }, 500);
    });
    renderPage("/policies?create=1&attach_key=vk-1&suggested_id=key-clinical-prod");
    const dialog = await screen.findByRole("dialog", { name: "Create Policy Attachment" });
    expect(within(dialog).getByLabelText("Attachment ID")).toHaveValue("key-clinical-prod");
    expect(within(dialog).getByRole("button", { name: "Remove key vk-1" })).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Scope impact preview")).toHaveTextContent("every configured dimension");
  });
});
