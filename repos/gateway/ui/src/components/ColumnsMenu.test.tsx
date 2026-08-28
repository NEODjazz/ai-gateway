import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ColumnsMenu } from "./ColumnsMenu";

function Harness() {
  const [visible, setVisible] = useState(new Set(["name"]));
  return <ColumnsMenu columns={[{ key: "name", label: "Name", locked: true }, { key: "description", label: "Description" }]} visible={visible} onChange={setVisible} />;
}

describe("ColumnsMenu", () => {
  it("marks visible columns and toggles optional columns without closing", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    expect(screen.getByRole("menuitemcheckbox", { name: "Name" })).toHaveAttribute("aria-checked", "true");
    const description = screen.getByRole("menuitemcheckbox", { name: "Description" });
    expect(description).toHaveAttribute("aria-checked", "false");
    await userEvent.click(description);
    expect(description).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("menu", { name: "Table columns" })).toBeInTheDocument();
  });
});
