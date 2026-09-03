import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { ActionsMenu } from "./ActionsMenu";

function renderMenu(menu: React.ReactNode) {
  return render(<MemoryRouter>{menu}</MemoryRouter>);
}

describe("ActionsMenu", () => {
  it("opens a compact menu and runs the selected action", async () => {
    const edit = vi.fn(); const remove = vi.fn();
    renderMenu(<ActionsMenu label="Actions for one" items={[{ label: "Edit", onSelect: edit }, { label: "Delete", tone: "danger", onSelect: remove }]} />);
    expect(screen.queryByRole("menuitem", { name: "Edit" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for one" }));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(remove).toHaveBeenCalledOnce(); expect(edit).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
  });

  it("supports keyboard opening, navigation and escape", async () => {
    renderMenu(<ActionsMenu label="Actions for keyboard row" items={[{ label: "Details", onSelect: () => {} }, { label: "Edit", onSelect: () => {} }]} />);
    const trigger = screen.getByRole("button", { name: "Actions for keyboard row" });
    trigger.focus(); await userEvent.keyboard("{ArrowDown}");
    const firstItem = await screen.findByRole("menuitem", { name: "Details" });
    await waitFor(() => expect(firstItem).toHaveFocus());
    await userEvent.keyboard("{ArrowDown}"); await waitFor(() => expect(screen.getByRole("menuitem", { name: "Edit" })).toHaveFocus());
    await userEvent.keyboard("{Escape}"); expect(trigger).toHaveFocus(); await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
  });

  it("routes inspect actions through React Router", async () => {
    render(<MemoryRouter initialEntries={["/api-keys"]}><ActionsMenu label="Actions for key" items={[{ label: "Inspect", href: "/api-keys/vk-1" }]} /><Routes><Route path="/api-keys" element={null} /><Route path="/api-keys/:id" element={<div>Virtual key details</div>} /></Routes></MemoryRouter>);
    await userEvent.click(screen.getByRole("button", { name: "Actions for key" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Inspect" }));
    expect(await screen.findByText("Virtual key details")).toBeInTheDocument();
  });
});
