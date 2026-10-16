import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CopyOutput, OutputDetails, outputDetails } from "./OutputDetails";

describe("Playground output details", () => {
  it("retains canonical file citations without treating tool input or reasoning as files", () => {
    const citation = { type: "container_file_citation", container_id: "cntr_demo", file_id: "cfile_demo", filename: "report.csv" };
    const details = outputDetails({ id: "resp_demo", output: [{ type: "message", role: "assistant", content: [{ type: "output_text", annotations: [citation, citation, { type: "file_citation", file_id: "file_demo", filename: "source.pdf" }] }] }, { type: "function_call", arguments: { annotations: [citation] } }, { type: "reasoning", annotations: [citation] }] });
    expect(details).toHaveProperty("files", [
      { fileID: "cfile_demo", containerID: "cntr_demo", filename: "report.csv" },
      { fileID: "file_demo", filename: "source.pdf" },
    ]);
  });
  it("bounds file lists, rejects malformed IDs and does not expose citations outside output text", () => {
    const annotation = { type: "container_file_citation", container_id: "cntr_demo", file_id: "cfile_demo", filename: "report" };
    const files = Array.from({ length: 33 }, (_, index) => ({ ...annotation, file_id: `file_${index}` }));
    const details = outputDetails({ output: [{ type: "message", content: [{ type: "output_text", annotations: [...files, { ...annotation, file_id: "../bad" }, { ...annotation, container_id: ".." }, { ...annotation, file_id: ".." }, null] }] }, { type: "function_call", annotations: [annotation] }] });
    expect(details.files).toHaveLength(32); expect(details.filesTruncated).toBe(true);
    render(<OutputDetails payload={{ output: [{ type: "message", content: [{ type: "output_text", annotations: files }] }] }} />);
    expect(screen.getByText("Only the first 32 file citations are shown.")).toBeInTheDocument();
  });
  it("renders citations and structured tools without inspecting tool arguments as output", () => {
    const payload = { output: [{ type: "function_call", name: "lookup", arguments: { type: "output_image", url: "https://hidden.example.test" } }, { type: "message", content: [{ type: "output_text", text: "Answer", annotations: [{ type: "url_citation", url: "https://docs.example.test", title: "Documentation" }, { type: "url_citation", url: "javascript:alert(1)" }, { type: "url_citation", url: "https://docs.example.test", title: "Duplicate" }] }] }] };
    expect(outputDetails(payload).images).toEqual([]);
    render(<OutputDetails payload={payload} />);
    expect(screen.getByText("Tool calls and results")).toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Documentation" })).toHaveAttribute("rel", "noopener noreferrer");
  });
  it("retains custom calls and results as tools without rendering their input as media or executing markup", () => {
    const calls = [{ type: "custom_tool_call", name: "query", input: '<script>alert("test")</script>', arguments: { type: "image", url: "https://hidden.example.test" } }, { type: "custom_tool_call_output", call_id: "call_1", output: '<img src="https://hidden.example.test">' }];
    const details = outputDetails({ output: calls }); expect(details.tools).toEqual(calls); expect(details.images).toEqual([]); expect(details.citations).toEqual([]);
    const view = render(<OutputDetails payload={{ output: calls }} />);
    expect(screen.getByText("Tool calls and results")).toBeInTheDocument(); expect(view.container.querySelector("script")).toBeNull(); expect(screen.queryByRole("img")).not.toBeInTheDocument();
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
