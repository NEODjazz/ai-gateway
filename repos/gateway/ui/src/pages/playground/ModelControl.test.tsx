import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ModelControl } from "./ModelControl";

describe("Playground model selection", () => {
  it("switches to manual input by keyboard without changing the current model", async () => {
    const update = vi.fn();
    render(<ModelControl label="Model" value="listed" models={["listed"]} scope="first" onUpdate={update} />);
    screen.getByRole("button", { name: "Model: enter ID manually" }).focus(); await userEvent.keyboard("{Enter}");
    expect(screen.getByRole("textbox", { name: "Model" })).toHaveValue("listed"); expect(update).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "explicit" } }); expect(update).toHaveBeenCalledWith("explicit");
  });
  it("restores catalog selection without replacing a model that is still listed", async () => {
    const update = vi.fn();
    render(<ModelControl label="Model" value="listed" models={["other", "listed"]} scope="first" onUpdate={update} />);
    await userEvent.click(screen.getByRole("button", { name: "Model: enter ID manually" }));
    await userEvent.click(screen.getByRole("button", { name: "Model: choose from catalog" }));
    expect(screen.getByLabelText("Model")).toHaveTextContent("listed"); expect(update).not.toHaveBeenCalled();
  });
  it.each(["disabled", "loading"] as const)("locks both manual entry and its mode switch while %s", async (lock) => {
    const update = vi.fn();
    render(<ModelControl label="Model" value="explicit" models={["listed"]} scope="first" onUpdate={update} {...{ [lock]: true }} />);
    expect(screen.getByLabelText("Model")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Model: choose from catalog" })); expect(update).not.toHaveBeenCalled();
  });
  it("resets manual mode on scope change and allows input when discovery has no models", async () => {
    const update = vi.fn();
    const view = render(<ModelControl label="Model" value="listed" models={["listed"]} scope="first" onUpdate={update} />);
    await userEvent.click(screen.getByRole("button", { name: "Model: enter ID manually" }));
    view.rerender(<ModelControl label="Model" value="new" models={["new"]} scope="second" onUpdate={update} />);
    expect(screen.queryByRole("textbox", { name: "Model" })).not.toBeInTheDocument(); expect(screen.getByLabelText("Model")).toHaveTextContent("new");
    view.rerender(<ModelControl label="Model" value="new" models={[]} scope="second" onUpdate={update} />);
    expect(screen.getByLabelText("Model")).toHaveValue("new"); expect(screen.queryByRole("button", { name: /Model:/ })).not.toBeInTheDocument(); expect(update).not.toHaveBeenCalled();
  });
});
