import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ColumnsMenu } from "./ColumnsMenu";
import "../styles.css";

function Harness() {
  const [visible, setVisible] = useState(new Set(["name"]));
  return <ColumnsMenu columns={[{ key: "name", label: "Name", locked: true }, { key: "description", label: "Description" }]} visible={visible} onChange={setVisible} />;
}

describe("ColumnsMenu", () => {
  it("marks visible columns and toggles optional columns without closing", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    const name = screen.getByRole("menuitemcheckbox", { name: "Name" });
    expect(name).toHaveAttribute("aria-checked", "true");
    expect(getComputedStyle(name).backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(getComputedStyle(name).color).not.toBe("rgb(255, 255, 255)");
    const description = screen.getByRole("menuitemcheckbox", { name: "Description" });
    expect(description).toHaveAttribute("aria-checked", "false");
    await userEvent.click(description);
    expect(description).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("menu", { name: "Table columns" })).toBeInTheDocument();
  });
});
