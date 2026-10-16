import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { document, fixture, json, show, store } from "./testUtils";

describe("Vector store documents", () => {
  it("pages documents without presenting a page length as an exact filtered total and previews escaped content", async () => {
    fixture((call) => {
      if (call.path.includes("/content")) return json({ file_id: "file_two", filename: "Guide.txt", attributes: {}, content: [{ type: "text", text: "<script>example</script>" }] });
      if (call.path.includes("/files?")) return call.path.includes("after=file_one") ? json({ data: [document("file_two")], has_more: false }) : json({ data: [document()], has_more: true, last_id: "file_one" });
    });
    show("?store=vs_owned&tab=documents");
    await screen.findByText("file_one.txt");
    expect(screen.getByText("Page 1 · 1 documents shown · More available")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Next documents" }));
    await screen.findByText("file_two.txt");
    await userEvent.click(screen.getByRole("button", { name: "Actions for document file_two" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Preview content" }));
    const dialog = screen.getByRole("dialog", { name: "Document content" });
    expect(await within(dialog).findByText("<script>example</script>")).toBeInTheDocument();
    expect(dialog.querySelector("script")).toBeNull();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("retains failed attribute edits and sends scalar constraints without dropping them", async () => {
    const { calls } = fixture((call) => call.method === "POST" && call.path.includes("/files/file_one") ? json({ error: { message: "Attributes unavailable" } }, 503) : undefined);
    show("?store=vs_owned&tab=documents"); await screen.findByText("file_one.txt");
    await userEvent.click(screen.getByRole("button", { name: "Actions for document file_one" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit attributes" }));
    const dialog = screen.getByRole("dialog", { name: "Edit document attributes" });
    fireEvent.change(within(dialog).getByLabelText("Attributes JSON"), { target: { value: '{"region":"eu","rank":3,"approved":true}' } });
    await userEvent.click(within(dialog).getByRole("button", { name: "Save attributes" }));
    await screen.findByText("Attributes unavailable");
    expect(within(dialog).getByLabelText("Attributes JSON")).toHaveValue('{"region":"eu","rank":3,"approved":true}');
    expect(calls.find((call) => call.method === "POST")?.body).toEqual({ attributes: { region: "eu", rank: 3, approved: true } });
  });

  it("shows per-document outcomes, stops on failure and retries only unconfirmed/remaining rows", async () => {
    let fail = true;
    const writes: string[] = [];
    fixture((call) => {
      if (call.path !== "/v1/rag/ingest") return;
      const name = (call.body?.file as { filename: string }).filename; writes.push(name);
      if (name === "two.txt" && fail) return json({ error: { message: "Second document failed" } }, 503);
      const id = "file_" + name.split(".")[0];
      return json({ vector_store: store(), attachment: document(id), file: { id } });
    });
    show("?store=vs_owned&tab=documents"); await screen.findByText("file_one.txt");
    await userEvent.upload(screen.getByLabelText("Documents", { exact: true }), [new File(["one"], "one.txt", { type: "text/plain" }), new File(["two"], "two.txt", { type: "text/plain" }), new File(["three"], "three.txt", { type: "text/plain" })]);
    await userEvent.click(screen.getByRole("button", { name: "Ingest documents" }));
    await screen.findByText("1 of 3 documents confirmed attached. A failed or timed-out request may have completed; refresh documents before retrying.");
    expect(writes).toEqual(["one.txt", "two.txt"]);
    expect(screen.getByText(/three.txt/).closest("li")).toHaveTextContent("Not attempted");
    fail = false;
    await userEvent.click(screen.getByRole("button", { name: "Retry failed and remaining documents" }));
    await screen.findByText("3 of 3 documents confirmed attached. A failed or timed-out request may have completed; refresh documents before retrying.");
    expect(writes).toEqual(["one.txt", "two.txt", "two.txt", "three.txt"]);
    expect(screen.getByLabelText("Documents", { exact: true })).toBeDisabled();
  });

  it("stops after an in-flight document without aborting it, then resumes the remaining queue", async () => {
    let resolveFirst!: (response: Response) => void;
    let writes = 0;
    fixture((call) => {
      if (call.path !== "/v1/rag/ingest") return;
      writes++;
      if (writes === 1) return new Promise((resolve) => { resolveFirst = resolve; });
      return json({ vector_store: store(), attachment: document("file_two"), file: { id: "file_two" } });
    });
    show("?store=vs_owned&tab=documents"); await screen.findByText("file_one.txt");
    await userEvent.upload(screen.getByLabelText("Documents", { exact: true }), [new File(["one"], "one.txt"), new File(["two"], "two.txt")]);
    await userEvent.click(screen.getByRole("button", { name: "Ingest documents" }));
    await waitFor(() => expect(resolveFirst).toBeTypeOf("function"));
    await userEvent.click(screen.getByRole("button", { name: "Stop after current document" }));
    await act(async () => resolveFirst(json({ vector_store: store(), attachment: document(), file: { id: "file_one" } })));
    await waitFor(() => expect(screen.getByRole("button", { name: "Ingest documents" })).toBeEnabled());
    expect(writes).toBe(1); expect(screen.getByText("two.txt").closest("li")).toHaveTextContent("Not attempted");
    await userEvent.click(screen.getByRole("button", { name: "Ingest documents" }));
    await screen.findByText("2 of 2 documents confirmed attached. A failed or timed-out request may have completed; refresh documents before retrying.");
    expect(writes).toBe(2);
  });

  it("cancels queued writes on credential change and ignores the old write completing later", async () => {
    let resolveFirst!: (response: Response) => void;
    const { calls } = fixture((call) => call.path === "/v1/rag/ingest" ? new Promise((resolve) => { resolveFirst = resolve; }) : undefined);
    show("?store=vs_owned&tab=documents"); await screen.findByText("file_one.txt");
    await userEvent.upload(screen.getByLabelText("Documents", { exact: true }), [new File(["one"], "one.txt"), new File(["two"], "two.txt")]);
    await userEvent.click(screen.getByRole("button", { name: "Ingest documents" }));
    await waitFor(() => expect(resolveFirst).toBeTypeOf("function"));
    await userEvent.selectOptions(screen.getByLabelText("Resource credential"), "key");
    expect(calls.find((call) => call.path === "/v1/rag/ingest")?.options?.signal?.aborted).toBe(true);
    await act(async () => resolveFirst(json({ vector_store: store(), attachment: document(), file: { id: "file_one" } })));
    expect(calls.filter((call) => call.path === "/v1/rag/ingest")).toHaveLength(1);
    expect(screen.queryByText(/confirmed attached/)).not.toBeInTheDocument();
  });

  it("pages owned existing files and preserves selected attributes and static chunking", async () => {
    const { calls } = fixture((call) => {
      if (call.path.startsWith("/v1/files?")) return call.path.includes("after=file_a") ? json({ data: [{ id: "file_b", filename: "Late document.txt", bytes: 4, purpose: "assistants" }], has_more: false }) : json({ data: [{ id: "file_a", filename: "First.txt", bytes: 4, purpose: "assistants" }], has_more: true, last_id: "file_a" });
      if (call.path === "/v1/rag/ingest") return json({ attachment: { ...document("file_b"), attributes: { region: "eu" }, chunking_strategy: { type: "static", static: { max_chunk_size_tokens: 800, chunk_overlap_tokens: 200 } } } });
    });
    show("?store=vs_owned&tab=documents"); await screen.findByText("file_one.txt");
    await userEvent.click(screen.getByRole("button", { name: "Attach existing file" }));
    const dialog = screen.getByRole("dialog", { name: "Attach existing file" });
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Next files" })).toBeEnabled());
    await userEvent.click(within(dialog).getByRole("button", { name: "Next files" }));
    await within(dialog).findByRole("option", { name: /Late document/ });
    await userEvent.selectOptions(within(dialog).getByLabelText("Existing file"), "file_b");
    fireEvent.change(within(dialog).getByLabelText("Attributes JSON"), { target: { value: '{"region":"eu"}' } });
    await userEvent.selectOptions(within(dialog).getByLabelText("Chunking"), "static");
    await userEvent.click(within(dialog).getByRole("button", { name: "Attach file" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(calls.find((call) => call.path === "/v1/rag/ingest")?.body).toEqual({ file_id: "file_b", vector_store_id: "vs_owned", attributes: { region: "eu" }, chunking_strategy: { type: "static", static: { max_chunk_size_tokens: 800, chunk_overlap_tokens: 200 } } });
  });
});
