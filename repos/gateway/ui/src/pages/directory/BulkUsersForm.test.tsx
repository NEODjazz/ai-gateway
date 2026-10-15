import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { APIError } from "../../api/client";
import { BulkUsersForm } from "./BulkUsersForm";

const request = vi.hoisted(() => vi.fn());
vi.mock("../../auth/AuthContext", () => ({ useAuth: () => ({ client: { request } }) }));
beforeEach(() => request.mockReset());
function show() {
  const onClose = vi.fn(), onCreated = vi.fn();
  return { ...render(<MemoryRouter><BulkUsersForm onClose={onClose} onCreated={onCreated} /></MemoryRouter>), onClose, onCreated };
}
async function preview(csv = "id,name,email\nalice,Alice,alice@example.test\nbob,Bob,bob@example.test") {
  fireEvent.change(screen.getByLabelText("CSV users"), { target: { value: csv } });
  await userEvent.click(screen.getByRole("button", { name: "Preview users" }));
}

it("previews without writes, shows partial outcomes and retries only failures with unchanged IDs", async () => {
  request.mockImplementation(async (_path, options) => { if (options.body.id === "bob") throw new APIError(409, "conflict", "Identity already exists"); return { id: options.body.id }; });
  const { onCreated } = show();
  await preview();
  expect(request).not.toHaveBeenCalled();
  await userEvent.selectOptions(screen.getByLabelText("Status for new users"), "disabled");
  await userEvent.click(screen.getByRole("button", { name: "Create 2 users" }));
  await screen.findByText("1 created · 1 failed · 0 not attempted");
  expect(onCreated).toHaveBeenCalledTimes(1);
  expect(request).toHaveBeenNthCalledWith(1, "/admin/v1/users", expect.objectContaining({ method: "POST", body: { id: "alice", name: "Alice", email: "alice@example.test", external_id: "", roles: ["user"], status: "disabled" } }));
  expect(screen.getByRole("link", { name: "alice" })).toHaveAttribute("href", "/users/alice");
  expect(screen.getByText("Identity already exists")).toBeInTheDocument();
  expect(screen.getByLabelText("Status for new users")).toBeDisabled();
  request.mockResolvedValue({ id: "bob" });
  await userEvent.click(screen.getByRole("button", { name: "Retry failed users" }));
  await screen.findByText("2 created · 0 failed · 0 not attempted");
  expect(request).toHaveBeenCalledTimes(3);
  expect(request.mock.calls.map((call) => call[1].body.id)).toEqual(["alice", "bob", "bob"]);
});

it("prevents dismissal and duplicate submissions during sequential creates", async () => {
  let resolve!:(value: unknown) => void;
  request.mockImplementation((_path, options) => options.body.id === "alice" ? new Promise((done) => { resolve = done; }) : Promise.resolve({ id: "bob" }));
  const { onClose } = show(); await preview();
  await userEvent.click(screen.getByRole("button", { name: "Create 2 users" }));
  const create = screen.getByRole("button", { name: "Create 1 users" });
  expect(create).toBeDisabled();
  await userEvent.click(create);
  await userEvent.keyboard("{Escape}");
  expect(onClose).not.toHaveBeenCalled();
  expect(request).toHaveBeenCalledTimes(1);
  await act(async () => resolve({ id: "alice" }));
  await screen.findByText("2 created · 0 failed · 0 not attempted");
  expect(request).toHaveBeenCalledTimes(2);
});

it("stops after authorization failure and labels remaining users as not attempted", async () => {
  request.mockRejectedValue(new APIError(403, "forbidden", "Not authorized"));
  const { onCreated } = show(); await preview();
  await userEvent.click(screen.getByRole("button", { name: "Create 2 users" }));
  await screen.findByText("0 created · 1 failed · 1 not attempted");
  expect(screen.getByRole("alert")).toHaveTextContent("Remaining users were not attempted");
  expect(request).toHaveBeenCalledTimes(1); expect(onCreated).not.toHaveBeenCalled();
});

it("stops queued creates and ignores completion after unmount", async () => {
  let resolve!:(value: unknown) => void;
  request.mockImplementation(() => new Promise((done) => { resolve = done; }));
  const { unmount, onCreated } = show(); await preview();
  await userEvent.click(screen.getByRole("button", { name: "Create 2 users" }));
  unmount();
  await act(async () => resolve({ id: "alice" }));
  expect(request).toHaveBeenCalledTimes(1); expect(onCreated).not.toHaveBeenCalled();
});

it("stops after the current write and resumes only users not yet attempted", async () => {
  let resolve!:(value: unknown) => void;
  request.mockImplementation(() => new Promise((done) => { resolve = done; }));
  show(); await preview();
  await userEvent.click(screen.getByRole("button", { name: "Create 2 users" }));
  await userEvent.click(screen.getByRole("button", { name: "Stop after current user" }));
  await act(async () => resolve({ id: "alice" }));
  await screen.findByText("1 created · 0 failed · 1 not attempted");
  expect(request).toHaveBeenCalledTimes(1);
  request.mockResolvedValue({ id: "bob" });
  await userEvent.click(screen.getByRole("button", { name: "Create 1 users" }));
  await screen.findByText("2 created · 0 failed · 0 not attempted");
  expect(request.mock.calls.map((call) => call[1].body.id)).toEqual(["alice", "bob"]);
});

it("rejects invalid CSV without writes and treats mismatched create responses as unconfirmed", async () => {
  show(); await preview("id,name\na,Alice\na,Another");
  expect(screen.getByRole("alert")).toHaveTextContent("duplicate id");
  expect(request).not.toHaveBeenCalled();
  await preview("email\nalice@example.test");
  const row = within(screen.getByRole("table", { name: "Bulk user outcomes" })).getAllByRole("row")[1];
  const generatedID = within(row).getAllByRole("cell")[0].textContent;
  request.mockResolvedValue({ id: "foreign-user" });
  await userEvent.click(screen.getByRole("button", { name: "Create 1 users" }));
  await screen.findByText(/Create returned a different identity/);
  request.mockImplementation(async (_path, options) => ({ id: options.body.id }));
  await userEvent.click(screen.getByRole("button", { name: "Retry failed users" }));
  await screen.findByText("1 created · 0 failed · 0 not attempted");
  expect(request.mock.calls.map((call) => call[1].body.id)).toEqual([generatedID, generatedID]);
});

it("does not overwrite pasted input with a delayed file read", async () => {
  let resolve!:(text: string) => void;
  const file = new File(["id\nold"], "users.csv", { type: "text/csv" });
  Object.defineProperty(file, "text", { value: () => new Promise<string>((done) => { resolve = done; }) });
  show();
  await userEvent.upload(screen.getByLabelText("Upload CSV"), file);
  expect(screen.getByRole("button", { name: "Preview users" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText("CSV users"), { target: { value: "id\nfresh" } });
  await act(async () => resolve("id\nstale"));
  expect(screen.getByLabelText("CSV users")).toHaveValue("id\nfresh");
  await userEvent.click(screen.getByRole("button", { name: "Preview users" }));
  expect(screen.getByText("fresh")).toBeInTheDocument();
  expect(screen.queryByText("stale")).not.toBeInTheDocument();
});
