import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ManagedDataTable } from "./ManagedDataTable";
import "../styles.css";

function SelectableHarness() {
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  return <ManagedDataTable rows={[{ id: "a", description: "Alpha" }, { id: "b", description: "Beta" }]} columns={[{ key: "id", label: "ID" }, { key: "description", label: "Description" }]} selection={{ selectedIds, onSelectionChange: setSelectedIds }} />;
}

describe("ManagedDataTable", () => {
  it("provides virtual-key-style search, columns, sorting, refresh and pagination", async () => {
    const refresh = vi.fn();
    render(<ManagedDataTable rows={[{ id: "b", description: "Beta" }, { id: "a", description: "Alpha" }]} columns={[{ key: "id", label: "ID" }, { key: "description", label: "Description" }]} onRefresh={refresh} searchPlaceholder="Search resources" />);
    expect(screen.getAllByRole("row")[1]).toHaveTextContent("a");
    const sortButton = screen.getByRole("button", { name: /ID/ });
    expect(getComputedStyle(sortButton).backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(sortButton).toHaveClass("sort-button");
    await userEvent.click(sortButton);
    expect(screen.getAllByRole("row")[1]).toHaveTextContent("b");
    await userEvent.type(screen.getByLabelText("Search resources"), "Alpha");
    expect(screen.getByText("a")).toBeInTheDocument(); expect(screen.queryByText("b")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Description" }));
    expect(screen.queryByRole("columnheader", { name: /Description/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh table" })); expect(refresh).toHaveBeenCalled();
    expect(screen.getByText("1–1 of 1")).toBeInTheDocument();
    const pageSizeSelect = screen.getByRole("combobox", { name: "Rows per page" });
    expect(pageSizeSelect.className).toContain("g-select-control__button");
    expect(getComputedStyle(pageSizeSelect).backgroundColor).not.toBe("rgb(15, 159, 131)");
  });

  it("uses the Gravity Table selection column with select-all and indeterminate states", async () => {
    render(<SelectableHarness />);
    const headerCheckbox = within(screen.getAllByRole("columnheader")[0]).getByRole("checkbox");
    const rowCheckboxes = screen.getAllByRole("checkbox").slice(1);
    expect(rowCheckboxes).toHaveLength(2);
    await userEvent.click(rowCheckboxes[0]);
    expect(rowCheckboxes[0]).toBeChecked();
    expect(headerCheckbox).toBePartiallyChecked();
    await userEvent.click(headerCheckbox);
    expect(headerCheckbox).toBeChecked();
    expect(rowCheckboxes.every((checkbox) => (checkbox as HTMLInputElement).checked)).toBe(true);
  });
});
