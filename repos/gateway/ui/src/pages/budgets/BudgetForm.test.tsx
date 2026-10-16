import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BudgetForm } from "./BudgetForm";

it("rejects token caps which JavaScript cannot represent exactly", async () => {
  const save = vi.fn();
  render(<BudgetForm scope={{type:"team",id:"platform"}} onClose={vi.fn()} onSave={save} />);
  const dialog = screen.getByRole("dialog",{name:"Create budget"});
  await userEvent.type(within(dialog).getByLabelText("Budget maximum tokens"),"9007199254740993");
  await userEvent.click(within(dialog).getByRole("button",{name:"Create budget"}));
  expect(await within(dialog).findByText("Limits must be positive; token limits must be whole numbers.")).toBeInTheDocument();
  expect(save).not.toHaveBeenCalled();
});
