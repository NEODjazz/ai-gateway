import { render, screen } from "@testing-library/react";
import { DataTable } from "./DataTable";

describe("DataTable", () => {
  it("renders its empty state", () => {
    render(<DataTable rows={[]} columns={[{ key: "id", label: "ID" }]} />);
    expect(screen.getByText("No records found.")).toBeInTheDocument();
  });

  it("renders booleans, arrays and objects safely", () => {
    render(<DataTable rows={[{ id: "one", enabled: true, tags: ["a", "b"], metadata: { safe: true } }]} columns={[{ key: "id", label: "ID" }, { key: "enabled", label: "Status" }, { key: "tags", label: "Tags" }, { key: "metadata", label: "Metadata" }]} />);
    expect(screen.getByText("Enabled")).toBeInTheDocument();
    expect(screen.getByText("a")).toBeInTheDocument();
    expect(screen.getByText('{"safe":true}')).toBeInTheDocument();
  });

  it("supports custom cells and actions", () => {
    render(<DataTable rows={[{ id: "one" }]} columns={[{ key: "id", label: "ID", render: (value) => <strong>{String(value)}</strong> }]} actions={() => <button>Edit</button>} />);
    expect(screen.getByText("one").tagName).toBe("STRONG");
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
  });
});
