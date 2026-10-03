import { conversationAttachments, conversationInput, retainConversation } from "./attachments";
describe("Conversation attachments and bounds", () => {
  it("encodes image and PDF into their distinct public content contracts", () => {
    expect(conversationInput("Describe", [{ filename: "image.png", media_type: "image/png", data_base64: "AA==" }, { filename: "file.pdf", media_type: "application/pdf", data_base64: "BB==" }])).toEqual([
      { type: "text", text: "Describe" }, { type: "image_url", image_url: { url: "data:image/png;base64,AA==" } }, { type: "input_file", filename: "file.pdf", file_data: "data:application/pdf;base64,BB==" }
    ]);
    expect(conversationInput("Plain", [])).toBe("Plain");
  });
  it("rejects excess files, bytes and unsafe filenames before reading", async () => {
    const file = new File(["image"], "image.png", { type: "image/png" });
    await expect(conversationAttachments(Array.from({ length: 6 }, () => file))).rejects.toThrow("5 attachments");
    await expect(conversationAttachments([new File(["x".repeat(8 * 1024 * 1024 + 1)], "image.png", { type: "image/png" })])).rejects.toThrow("8 MiB");
    await expect(conversationAttachments([new File(["image"], "../image.png", { type: "image/png" })])).rejects.toThrow("filename");
  });
  it("retains whole conversation pairs at the turn and byte boundaries", () => {
    const turns = Array.from({ length: 50 }, (_, index) => ({ role: index % 2 ? "assistant" : "user", text: String(index) }));
    expect(retainConversation(turns)).toEqual({ turns: turns.slice(10), dropped: 10 });
    const large = Array.from({ length: 10 }, (_, index) => ({ role: index % 2 ? "assistant" : "user", text: "x".repeat(4 * 1024 * 1024) }));
    const retained = retainConversation(large);
    expect(retained.dropped % 2).toBe(0); expect(retained.turns[0].role).toBe("user"); expect(new TextEncoder().encode(JSON.stringify(retained.turns)).length).toBeLessThanOrEqual(32 * 1024 * 1024);
  });
});
