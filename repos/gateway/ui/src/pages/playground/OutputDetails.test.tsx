import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CopyOutput, OutputDetails, outputDetails } from "./OutputDetails";

describe("Playground output details", () => {
  it("renders citations and structured tools without inspecting tool arguments as output", () => {
    const payload = { output: [{ type: "function_call", name: "lookup", arguments: { type: "output_image", url: "https://hidden.example.test" } }, { type: "message", content: [{ type: "output_text", text: "Answer", annotations: [{ type: "url_citation", url: "https://docs.example.test", title: "Documentation" }, { type: "url_citation", url: "javascript:alert(1)" }, { type: "url_citation", url: "https://docs.example.test", title: "Duplicate" }] }] }] };
    expect(outputDetails(payload).images).toEqual([]);
    render(<OutputDetails payload={payload} />);
    expect(screen.getByText("Tool calls and results")).toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Documentation" })).toHaveAttribute("rel", "noopener noreferrer");
  });
  it("accepts safe image outputs and rejects unsafe schemes and credentials", () => {
    const details = outputDetails({ content: [{ type: "image", data: "AA==", mime_type: "image/png" }, { type: "image_url", image_url: { url: "https://user:secret@example.test/image" } }, { type: "output_image", url: "file:///private/image.png" }] });
    expect(details.images).toEqual(["data:image/png;base64,AA=="]);
  });
  it("finds tools and safe media inside interaction steps and agent parts", () => {
    const details = outputDetails({ steps: [{ type: "function_call", id: "call", name: "lookup", arguments: { type: "image", url: "https://hidden.example.test" } }, { type: "model_output", content: [{ type: "image", data: "AA==", mime_type: "image/png" }] }], parts: [{ type: "output_text", annotations: [{ type: "url_citation", url: "https://docs.example.test", title: "Docs" }] }] });
    expect(details.tools).toHaveLength(1); expect(details.images).toEqual(["data:image/png;base64,AA=="]); expect(details.citations).toEqual([{ url: "https://docs.example.test/", title: "Docs" }]);
  });
  it("copies the exact output and reports clipboard failure visibly", async () => {
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    render(<CopyOutput text={'Answer\n"quoted"'} />);
    await user.click(screen.getByRole("button", { name: "Copy output" }));
    expect(write).toHaveBeenCalledWith('Answer\n"quoted"');
    expect(screen.getByRole("status")).toHaveTextContent("Copied");
    write.mockRejectedValue(new Error("Denied"));
    await user.click(screen.getByRole("button", { name: "Copy output" }));
    expect(screen.getByRole("status")).toHaveTextContent("Could not copy output");
  });
});
