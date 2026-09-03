import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ActionsMenu } from "./ActionsMenu";

function renderMenu(menu: React.ReactNode) {
  return render(menu);
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

  it("prefixes inspect links with the console base path", async () => {
    renderMenu(<ActionsMenu label="Actions for key" items={[{ label: "Inspect", href: "/api-keys/vk-1" }]} />);
    await userEvent.click(screen.getByRole("button", { name: "Actions for key" }));
    expect(screen.getByRole("menuitem", { name: "Inspect" })).toHaveAttribute("href", "/ui/api-keys/vk-1");
  });
});
