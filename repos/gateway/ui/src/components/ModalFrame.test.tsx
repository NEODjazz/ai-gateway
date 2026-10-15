import { useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourceForm } from "./ResourceForm";

it("restores focus when closed before the opening transition finishes", async () => {
  function Harness() {
    const [open, setOpen] = useState(false);
    return <><button onClick={() => setOpen(true)}>Open early form</button>{open && <ResourceForm title="Early form" fields={[{ key: "name", label: "Name" }]} onClose={() => setOpen(false)} onSubmit={async () => {}} />}</>;
  }
  render(<Harness />);
  const opener = screen.getByRole("button", { name: "Open early form" });
  opener.focus();
  fireEvent.click(opener);
  screen.getByRole("textbox", { name: "Name" }).focus();
  fireEvent.keyDown(document.activeElement!, { key: "Escape" });
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  await waitFor(() => expect(opener).toHaveFocus());
});

it("contains keyboard focus, dismisses with Escape and restores the opener", async () => {
  function Harness() {
    const [open, setOpen] = useState(false);
    return <><button onClick={() => setOpen(true)}>Open form</button><button>Background</button>{open && <ResourceForm title="Edit record" fields={[{ key: "name", label: "Name" }]} onClose={() => setOpen(false)} onSubmit={async () => {}} />}</>;
  }
  render(<Harness />);
  const opener = screen.getByRole("button", { name: "Open form" });
  await userEvent.click(opener);
  const dialog = await screen.findByRole("dialog", { name: "Edit record" });
  await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
  for (let index = 0; index < 8; index++) {
    await userEvent.tab();
    await waitFor(() => expect(dialog.contains(document.activeElement), document.activeElement?.outerHTML).toBe(true));
  }
  await userEvent.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  await waitFor(() => expect(opener).toHaveFocus());
});
