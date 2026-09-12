import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { SkillsPage } from "./SkillsPage";

const custom = { id: "skill_custom", type: "skill", display_name: "Release checks", latest_version_id: "v1", source: { type: "custom" } };
const shared = { id: "skill_shared", type: "skill", display_name: "Shared guide", latest_version_id: "v3", source: { type: "anthropic" } };
const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));

describe("SkillsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("shows bounded inventory and sends multipart create and typed update requests", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (path === "/v1/skills?limit=1000") return json({ data: [custom, shared], has_more: true });
      if (path === "/v1/skills" && options?.method === "POST") return json(custom);
      if (path === "/v1/skills/skill_custom" && options?.method === "POST") return json({ ...custom, latest_version_id: "v2" });
      return json({ error: { message: "Unexpected request" } }, 500);
    });
    render(<AuthProvider><SkillsPage /></AuthProvider>);
    expect(await screen.findByText("Release checks")).toBeInTheDocument();
    expect(screen.getByText("Shared guide")).toBeInTheDocument();
    expect(screen.getByText("Additional skills are available")).toBeInTheDocument();
    expect(within(screen.getByText("Loaded").closest("article")!).getByText("2")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Create skill" }));
    const createForm = screen.getByRole("dialog", { name: "Create skill" });
    await userEvent.type(within(createForm).getByLabelText("Skill display name"), "Release checks");
    await userEvent.upload(within(createForm).getByLabelText("Skill files"), [new File(["one"], "SKILL.md"), new File(["two"], "check.sh")]);
    expect(within(createForm).getByText("2 files selected")).toBeInTheDocument();
    await userEvent.click(within(createForm).getByRole("button", { name: "Create skill" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => path === "/v1/skills" && options?.method === "POST")).toBe(true));
    const upload = fetchMock.mock.calls.find(([path, options]) => path === "/v1/skills" && options?.method === "POST");
    expect(upload?.[1]?.body).toBeInstanceOf(FormData);
    expect(new Headers(upload?.[1]?.headers).has("Content-Type")).toBe(false);
    expect((upload?.[1]?.body as FormData).getAll("files")).toHaveLength(2);

    await userEvent.click(screen.getByRole("button", { name: "Actions for skill skill_custom" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Set default version" }));
    const updateForm = screen.getByRole("dialog", { name: "Update Release checks" });
    const version = within(updateForm).getByLabelText("Default version ID");
    await userEvent.clear(version); await userEvent.type(version, "v2");
    await userEvent.click(within(updateForm).getByRole("button", { name: "Save default" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => path === "/v1/skills/skill_custom" && options?.method === "POST")).toBe(true));
    const update = fetchMock.mock.calls.find(([path, options]) => path === "/v1/skills/skill_custom" && options?.method === "POST");
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({ default_version: "v2" });

    await userEvent.click(screen.getByRole("button", { name: "Actions for skill skill_shared" }));
    expect(screen.getAllByRole("menuitem", { name: "Set default version" }).some((item) => item.getAttribute("aria-disabled") === "true")).toBe(true);
    expect(screen.getAllByRole("menuitem", { name: "Delete" }).some((item) => item.getAttribute("aria-disabled") === "true")).toBe(true);
  });

  it("downloads the owner-checked current skill archive", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/v1/skills?limit=1000"
      ? json({ data: [custom], has_more: false })
      : Promise.resolve(new Response("archive", { status: 200, headers: { "Content-Type": "application/zip" } })));
    const createObjectURL = vi.fn(() => "blob:skill");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(<AuthProvider><SkillsPage /></AuthProvider>);
    await screen.findByText("Release checks");
    await userEvent.click(screen.getByRole("button", { name: "Actions for skill skill_custom" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Download" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path === "/v1/skills/skill_custom/content")).toBe(true));
    expect(createObjectURL).toHaveBeenCalledOnce(); expect(click).toHaveBeenCalledOnce(); expect(revokeObjectURL).toHaveBeenCalledWith("blob:skill");
  });

  it("does not replace a newer inventory with a delayed refresh", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    let call = 0;
    let resolveOlder: ((response: Response) => void) | undefined;
    let resolveNewer: ((response: Response) => void) | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
      call++;
      if (call === 1) return json({ data: [custom], has_more: false });
      return new Promise<Response>((resolve) => { if (call === 2) resolveOlder = resolve; else resolveNewer = resolve; });
    });
    render(<AuthProvider><SkillsPage /></AuthProvider>);
    await screen.findByText("Release checks");
    await userEvent.click(screen.getByRole("button", { name: "Refresh table" }));
    await userEvent.click(screen.getByRole("button", { name: "Refresh table" }));
    await waitFor(() => expect(resolveOlder).toBeDefined()); await waitFor(() => expect(resolveNewer).toBeDefined());
    resolveNewer!(new Response(JSON.stringify({ data: [{ ...custom, display_name: "Newest" }], has_more: false }), { status: 200 }));
    expect(await screen.findByText("Newest")).toBeInTheDocument();
    resolveOlder!(new Response(JSON.stringify({ data: [{ ...custom, display_name: "Stale" }], has_more: false }), { status: 200 }));
    await waitFor(() => expect(screen.queryByText("Stale")).not.toBeInTheDocument());
    expect(screen.getByText("Newest")).toBeInTheDocument();
  });
});
