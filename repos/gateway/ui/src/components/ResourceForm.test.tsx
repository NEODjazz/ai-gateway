import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourceForm } from "./ResourceForm";

describe("ResourceForm", () => {
  it("converts csv, number, boolean and JSON values", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    render(<ResourceForm title="Edit resource" fields={[{ key: "tags", label: "Tags", type: "csv" }, { key: "limit", label: "Limit", type: "number" }, { key: "enabled", label: "Enabled", type: "boolean" }, { key: "metadata", label: "Metadata", type: "json" }]} onClose={() => {}} onSubmit={submit} />);
    await userEvent.type(screen.getByLabelText("Tags"), "one, two");
    await userEvent.clear(screen.getByLabelText("Limit")); await userEvent.type(screen.getByLabelText("Limit"), "7");
    await userEvent.click(screen.getByLabelText("Enabled"));
    fireEvent.change(screen.getByLabelText("Metadata"), { target: { value: '{"safe":true}' } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledWith({ tags: ["one", "two"], limit: 7, enabled: true, metadata: { safe: true } });
  });

  it("shows JSON parsing errors without submitting", async () => {
    const submit = vi.fn();
    render(<ResourceForm title="Edit resource" fields={[{ key: "metadata", label: "Metadata", type: "json" }]} onClose={() => {}} onSubmit={submit} />);
    fireEvent.change(screen.getByLabelText("Metadata"), { target: { value: "{" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
  });

  it("closes from the accessible close button", async () => {
    const close = vi.fn();
    render(<ResourceForm title="Edit resource" fields={[]} onClose={close} onSubmit={async () => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(close).toHaveBeenCalledOnce();
  });
});
