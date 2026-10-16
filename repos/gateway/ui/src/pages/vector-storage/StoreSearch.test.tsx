import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { fixture, json, show, store } from "./testUtils";

describe("Vector store search and editing", () => {
  it("submits the selected embedding model and filters, retains successful results after failure and bounds history", async () => {
    let fail = false;
    const { calls } = fixture((call) => {
      if (call.path.endsWith("/search")) return fail ? json({ error: { message: "Embedding quota exhausted" } }, 429) : json({ object: "vector_store.search_results.page", data: call.body?.query === "empty" ? [] : [{ file_id: "file_one", filename: "manual.txt", score: 0.75, attributes: { region: "eu" }, content: [{ type: "text", text: "<script>document text</script>" }] }], has_more: false });
    });
    show("?store=vs_owned&tab=test");
    await screen.findByRole("heading", { name: "Test vector store" });
    await screen.findByRole("button", { name: "Embedding model: enter ID manually" });
    await userEvent.click(screen.getByRole("button", { name: "Embedding model: enter ID manually" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Embedding model" }), "embedding-a");
    fireEvent.change(screen.getByLabelText("Search query"), { target: { value: "alpha" } });
    await userEvent.click(screen.getByText("Attribute filters", { selector: "summary" }));
    fireEvent.change(screen.getByLabelText("Filters JSON"), { target: { value: '{"type":"eq","key":"region","value":"eu"}' } });
    fireEvent.change(screen.getByLabelText("Maximum results"), { target: { value: "3" } });
    await userEvent.click(screen.getByRole("button", { name: "Run vector search" }));
    await screen.findByText("manual.txt · Score 0.7500");
    expect(calls.find((call) => call.path.endsWith("/search"))?.body).toEqual({ model: "embedding-a", query: "alpha", max_num_results: 3, filters: { type: "eq", key: "region", value: "eu" } });
    expect(screen.getByText("<script>document text</script>").tagName).toBe("PRE");
    fail = true;
    fireEvent.change(screen.getByLabelText("Search query"), { target: { value: "failed query" } });
    await userEvent.click(screen.getByRole("button", { name: "Run vector search" }));
    await screen.findByText("Embedding quota exhausted");
    expect(screen.getByRole("heading", { name: "alpha" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "failed query" })).not.toBeInTheDocument();
    fail = false;
    for (const query of ["second", "third", "fourth", "fifth", "empty"]) {
      fireEvent.change(screen.getByLabelText("Search query"), { target: { value: query } });
      await userEvent.click(screen.getByRole("button", { name: "Run vector search" }));
      await screen.findByRole("heading", { name: query });
    }
    const results = screen.getByRole("region", { name: "Vector search results" });
    expect(within(results).getAllByRole("heading", { level: 3 })).toHaveLength(5);
    expect(within(results).queryByRole("heading", { name: "alpha" })).not.toBeInTheDocument();
    expect(screen.getByText("No matching documents.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear history" }));
    expect(screen.getByText("No search has completed yet.")).toBeInTheDocument();
  });

  it("preserves metadata and expiry when editing and blocks keyboard dismissal while saving", async () => {
    let finish!: (response: Response) => void;
    const original = { ...store(), expires_after: { anchor: "last_active_at", days: 7 }, expires_at: 1792627200 };
    const { calls } = fixture((call) => {
      if (call.path === "/v1/vector_stores/vs_owned") return call.method === "GET" ? json(original) : new Promise((resolve) => { finish = resolve; });
    });
    show("?store=vs_owned");
    await userEvent.click(await screen.findByRole("button", { name: "Edit store" }));
    const dialog = screen.getByRole("dialog", { name: "Edit vector store" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Updated manuals" } });
    await userEvent.click(within(dialog).getByRole("button", { name: "Save vector store" }));
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    await userEvent.keyboard("{Escape}");
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(calls.find((call) => call.method === "POST")?.body).toEqual({ name: "Updated manuals", metadata: original.metadata, expires_after: { anchor: "last_active_at", days: 7 } });
    finish(json({ error: { message: "Write conflict" } }, 409));
    await screen.findByText("Write conflict");
    expect(within(dialog).getByLabelText("Name")).toHaveValue("Updated manuals");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
