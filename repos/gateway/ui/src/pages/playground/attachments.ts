import { readAttachment, type Attachment } from "./endpointRequests";

export async function conversationAttachments(files: File[]): Promise<Attachment[]> {
  if (files.length > 5 || files.reduce((size, file) => size + file.size, 0) > 8 * 1024 * 1024) throw new Error("Choose at most 5 attachments, up to 8 MiB in total.");
  for (const file of files) if (file.name.length > 256 || /[\\/\x00]/.test(file.name)) throw new Error("Attachment filename is invalid.");
  return Promise.all(files.map((file) => readAttachment(file, file.type === "application/pdf" ? "document" : "image")));
}

export function conversationInput(text: string, attachments: Attachment[]): unknown {
  if (!attachments.length) return text;
  return [{ type: "text", text }, ...attachments.map((file) => file.media_type === "application/pdf"
    ? { type: "input_file", filename: file.filename, file_data: `data:application/pdf;base64,${file.data_base64}` }
    : { type: "image_url", image_url: { url: `data:${file.media_type};base64,${file.data_base64}` } })];
}

// A user turn starts a group, including all assistant/tool continuations.
export function retainConversation<T extends { role: string }>(turns: T[], serialize: (value: unknown) => string = JSON.stringify): { turns: T[]; dropped: number } {
  const retained = turns.slice();
  let dropped = 0;
  while (retained.length > 40 || new TextEncoder().encode(serialize(retained)).length > 32 * 1024 * 1024) {
    const boundary = retained.findIndex((turn, index) => index > 0 && turn.role === "user");
    if (boundary < 0) throw new Error("The latest conversation group exceeds the 40-turn or 32 MiB history limit.");
    retained.splice(0, boundary); dropped += boundary;
  }
  return { turns: retained, dropped };
}
