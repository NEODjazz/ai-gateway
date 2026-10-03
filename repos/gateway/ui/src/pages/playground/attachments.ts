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

// Drop whole user/assistant pairs so retained history starts at a user boundary.
export function retainConversation<T>(turns: T[]): { turns: T[]; dropped: number } {
  const retained = turns.slice(-40);
  let dropped = turns.length - retained.length;
  while (retained.length > 2 && new TextEncoder().encode(JSON.stringify(retained)).length > 32 * 1024 * 1024) { retained.splice(0, 2); dropped += 2; }
  if (new TextEncoder().encode(JSON.stringify(retained)).length > 32 * 1024 * 1024) throw new Error("The latest conversation turn exceeds the 32 MiB history limit.");
  return { turns: retained, dropped };
}
