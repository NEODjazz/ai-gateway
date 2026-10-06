import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MarkdownOutput } from "./MarkdownOutput";

describe("MarkdownOutput", () => {
  it("renders headings, inline formatting, lists, quotes and GFM tables", () => {
    const { container } = render(<MarkdownOutput text={'# Review\n\n**Important** and *optional* with `inline()` and ~~obsolete~~.\n\n- First\n- Second\n\n1. Ordered\n\n> Quoted\n\n| Risk | Action |\n| --- | --- |\n| High | Check |\n\n- [x] Done'} />);
    expect(screen.getByRole("heading", { name: "Review", level: 1 })).toBeVisible();
    expect(container.querySelector("strong")).toHaveTextContent("Important");
    expect(container.querySelector("em")).toHaveTextContent("optional");
    expect(container.querySelector("code")).toHaveTextContent("inline()");
    expect(container.querySelector("del")).toHaveTextContent("obsolete");
    expect(container.querySelector("blockquote")).toHaveTextContent("Quoted");
    expect(screen.getAllByRole("list")).toHaveLength(3);
    expect(within(screen.getByRole("table")).getByRole("cell", { name: "High" })).toBeVisible();
    expect(screen.getByRole("checkbox")).toBeChecked();
    expect(screen.getByRole("checkbox")).toBeDisabled();
  });

  it("copies fenced code with indentation and displays its language", async () => {
    const user = userEvent.setup();
    const copy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const code = 'func main() {\n  fmt.Println("<hello>")\n}\n';
    render(<MarkdownOutput text={'```go\n' + code + '```'} />);
    expect(screen.getByLabelText("go code").textContent).toBe(code);
    expect(screen.getByText("go")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Copy code" }));
    expect(copy).toHaveBeenCalledWith(code);
    expect(screen.getByRole("status")).toHaveTextContent("Copied");
  });

  it("supports indented blocks and unlabeled fences", () => {
    const { container } = render(<MarkdownOutput text={'    indented\n\n```\nfenced\n```'} />);
    expect(container.querySelectorAll("pre code")).toHaveLength(2);
    expect(screen.getAllByRole("button", { name: "Copy code" })).toHaveLength(2);
  });

  it("renders unfinished streaming fences and updates the copied content", async () => {
    const user = userEvent.setup();
    const copy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const { rerender } = render(<MarkdownOutput text={'## Streaming\n\n```python\nprint('} />);
    expect(screen.getByLabelText("python code").textContent).toBe("print(\n");
    await user.click(screen.getByRole("button", { name: "Copy code" }));
    rerender(<MarkdownOutput text={'## Streaming\n\n```python\nprint("done")\n```\n\nFinished.'} />);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy code" }));
    expect(copy).toHaveBeenLastCalledWith('print("done")\n');
    expect(screen.getByText("Finished.")).toBeVisible();
  });

  it("reports clipboard failures without changing code", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("Denied"));
    render(<MarkdownOutput text={'```\noriginal\n```'} />);
    await user.click(screen.getByRole("button", { name: "Copy code" }));
    expect(screen.getByRole("status")).toHaveTextContent("Could not copy code");
    expect(screen.getByLabelText("Code block")).toHaveTextContent("original");
  });

  it("keeps untrusted HTML inert and does not load Markdown images", () => {
    const { container } = render(<MarkdownOutput text={'<script>alert(1)</script>\n\n<img src="https://tracker.test/pixel" onerror="alert(1)">\n\n![tracking image](https://tracker.test/pixel)\n\n```html\n<iframe src="https://tracker.test"></iframe>\n```'} />);
    expect(container.querySelector("script,img,iframe")).toBeNull();
    expect(screen.getByText("[Image: tracking image]")).toBeVisible();
    expect(screen.getByLabelText("html code").textContent).toContain("<iframe");
  });

  it.each(['javascript:alert%281%29', 'data:text/html;base64,PHNjcmlwdD4=', 'file:///tmp/private', '/admin/v1/danger', '//tracker.test/pixel'])('rejects unsafe or implicit link %s', (url) => {
    render(<MarkdownOutput text={`[unsafe](${url})`} />);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getByText("unsafe")).toBeVisible();
  });

  it("renders safe links, autolinks and footnotes", () => {
    render(<MarkdownOutput text={'[Docs](https://example.test/docs) and https://example.test/auto\n\nA footnote[^1].\n\n[^1]: Note.'} />);
    const link = screen.getByRole("link", { name: "Docs" });
    expect(link).toHaveAttribute("href", "https://example.test/docs");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(screen.getByRole("link", { name: "https://example.test/auto" })).toBeVisible();
    expect(screen.getByRole("link", { name: "1" })).toHaveAttribute("href", "#user-content-fn-1");
  });
});
