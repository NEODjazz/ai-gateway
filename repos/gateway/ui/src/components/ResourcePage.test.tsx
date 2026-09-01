import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { ResourcePage, type ResourceConfig } from "./ResourcePage";

const config: ResourceConfig = {
  eyebrow: "Test",
  title: "Projects",
  description: "Project records",
  listPath: "/admin/v1/projects",
  itemPath: (id) => `/admin/v1/projects/${id}`,
  deletePath: (id) => `/admin/v1/projects/${id}`,
  columns: [{ key: "id", label: "ID" }, { key: "name", label: "Name" }],
  fields: [{ key: "id", label: "ID", required: true }, { key: "name", label: "Name", required: true }]
};

function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "test-token");
  return render(<AuthProvider><ResourcePage config={config} /></AuthProvider>);
}

describe("ResourcePage", () => {
  it("loads and displays API records", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [{ id: "one", name: "One" }] }), { status: 200 }));
    renderPage();
    expect(await screen.findByText("One")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "ID" })).toBeInTheDocument();
  });

  it("uses the virtual-key table format for managed resources", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [{ id: "one", name: "One" }] }), { status: 200 }));
    sessionStorage.setItem("ai-gateway.admin-token", "test-token");
    render(<AuthProvider><ResourcePage config={{ ...config, managedTable: true, createLabel: "Create Project" }} /></AuthProvider>);
    expect(await screen.findByText("One")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Project" })).toBeInTheDocument();
    expect(screen.getByLabelText("Search projects")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Columns" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh table" })).toBeInTheDocument();
    expect(screen.getByLabelText("Rows per page")).toBeInTheDocument();
  });

  it("keeps scoped directory views read-only", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [{ id: "one", name: "One" }] }), { status: 200 }));
    sessionStorage.setItem("ai-gateway.admin-token", "test-token");
    render(<AuthProvider><ResourcePage config={{ ...config, managedTable: true }} readOnly /></AuthProvider>);
    expect(await screen.findByText("One")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Actions for one" })).not.toBeInTheDocument();
  });

  it("can hide create while retaining scoped row actions", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [{ id: "one", name: "One" }] }), { status: 200 }));
    sessionStorage.setItem("ai-gateway.admin-token", "test-token");
    render(<AuthProvider><ResourcePage config={{ ...config, managedTable: true }} allowCreate={false} /></AuthProvider>);
    expect(await screen.findByText("One")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Actions for one" })).toBeInTheDocument();
  });

  it("renders a bounded loading error and retries", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response(JSON.stringify({ error: { message: "Unavailable" } }), { status: 503 })).mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }));
    renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("Unavailable");
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(await screen.findByText("No records found.")).toBeInTheDocument();
  });

  it("creates a resource through the reusable form", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 })).mockResolvedValueOnce(new Response(JSON.stringify({ id: "new", name: "New" }), { status: 200 })).mockResolvedValueOnce(new Response(JSON.stringify({ data: [{ id: "new", name: "New" }] }), { status: 200 }));
    renderPage();
    await screen.findByText("No records found.");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await userEvent.type(screen.getByLabelText("ID"), "new");
    await userEvent.type(screen.getByLabelText("Name"), "New");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    const [, options] = fetchMock.mock.calls[1];
    expect(options?.method).toBe("PUT");
    expect(options?.body).toBe('{"name":"New"}');
  });

  it("edits an existing row", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response(JSON.stringify({ data: [{ id: "one", name: "One" }] }), { status: 200 })).mockResolvedValueOnce(new Response(JSON.stringify({ id: "one", name: "Updated" }), { status: 200 })).mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }));
    renderPage();
    await screen.findByText("One");
    await userEvent.click(screen.getByRole("button", { name: "Actions for one" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    await userEvent.clear(screen.getByLabelText("Name")); await userEvent.type(screen.getByLabelText("Name"), "Updated");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock.mock.calls[1][0]).toBe("/admin/v1/projects/one");
  });

  it("deletes after confirmation", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response(JSON.stringify({ data: [{ id: "one", name: "One" }] }), { status: 200 })).mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }));
    renderPage();
    await screen.findByText("One");
    await userEvent.click(screen.getByRole("button", { name: "Actions for one" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock.mock.calls[1][1]?.method).toBe("DELETE");
  });

  it("reloads after a revision conflict event", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [] }), { status: 200 }));
    renderPage(); await screen.findByText("No records found.");
    act(() => window.dispatchEvent(new CustomEvent("control-plane-conflict")));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});
