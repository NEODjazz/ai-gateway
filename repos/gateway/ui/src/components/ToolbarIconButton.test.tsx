import { render, screen } from "@testing-library/react";
import { ToolbarIconButton } from "./ToolbarIconButton";

describe("ToolbarIconButton", () => {
  it("exposes active filters without a custom color indicator", () => {
    const { container } = render(<ToolbarIconButton icon="filter" label="Filter" active />);

    expect(screen.getByRole("button", { name: "Filter" })).toHaveAttribute("aria-pressed", "true");
    expect(container.querySelector(".gateway-icon-button-indicator")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Filter" })).not.toHaveClass("active");
  });
});
