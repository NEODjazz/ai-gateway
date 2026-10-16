import { useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourceForm } from "./ResourceForm";
import { ModalFrame } from "./ModalFrame";

it("focuses immediately and preserves typing when the animated focus manager starts", async () => {
  vi.useFakeTimers();
  try {
    render(<ModalFrame label="Early typing" onClose={() => {}}><input aria-label="Draft name" /></ModalFrame>);
    const dialog = screen.getByRole("dialog", {name:"Early typing"});
    await act(async () => { await vi.advanceTimersByTimeAsync(50); });
    expect(dialog).toHaveFocus();
    const input = screen.getByRole("textbox", {name:"Draft name"});
    input.focus(); fireEvent.change(input,{target:{value:"Typing continues"}});
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(input).toHaveValue("Typing continues");
    expect(input).toHaveFocus();
  } finally { vi.useRealTimers(); }
});

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
