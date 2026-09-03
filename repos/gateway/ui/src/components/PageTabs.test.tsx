import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PageTabs } from "./PageTabs";

describe("PageTabs", () => {
  it("uses the standard Gravity tab state and reports changes", async () => {
    const onUpdate = vi.fn();
    render(<PageTabs label="Log type" value="requests" items={[{ value: "requests", label: "Requests" }, { value: "audit", label: "Audit" }]} onUpdate={onUpdate} />);

    const requests = screen.getByRole("tab", { name: "Requests" });
    expect(requests).toHaveAttribute("aria-selected", "true");
    expect(requests).toHaveClass("g-tab_active");
    expect(requests).not.toHaveClass("active");

    await userEvent.click(screen.getByRole("tab", { name: "Audit" }));
    expect(onUpdate).toHaveBeenCalledWith("audit");
  });
});
