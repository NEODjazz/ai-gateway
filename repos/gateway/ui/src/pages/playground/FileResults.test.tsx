import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { FileResults, checkedImageType, outputFilename, outputFilePath } from "./FileResults";
import type { PlaygroundConnection } from "./requests";

const file = { fileID: "cfile_demo", containerID: "cntr_demo", filename: "report.png" };
const png = Uint8Array.from([137, 80, 78, 71, 13, 10, 26, 10]);
beforeEach(() => {
  Object.defineProperty(URL, "createObjectURL", { configurable: true, value: vi.fn() });
  Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
  // jsdom's Blob lacks arrayBuffer; retain real binary bytes through Response.
  vi.spyOn(Blob.prototype, "slice").mockImplementation(function (this: Blob, start, end) {
    const blob = this;
    return { arrayBuffer: async () => {
      const reader = new FileReader();
      return new Promise<ArrayBuffer>((resolve, reject) => { reader.onload = () => resolve((reader.result as ArrayBuffer).slice(start, end)); reader.onerror = reject; reader.readAsArrayBuffer(blob); });
    } } as Blob;
  });
});
afterEach(() => { Reflect.deleteProperty(URL, "createObjectURL"); Reflect.deleteProperty(URL, "revokeObjectURL"); });
function connection(): PlaygroundConnection { return { client: new APIClient(() => "test-key", { credentials: "omit", sessionEvents: false }), path: (path) => `/test${path}`, source: "custom", baseURL: "" }; }

describe("Response file results", () => {
  it("uses owned-resource paths and rejects untrusted IDs or unstored generated outputs", () => {
    expect(outputFilePath(file, "resp_demo", true)).toBe("/v1/responses/resp_demo/containers/cntr_demo/files/cfile_demo/content");
    expect(outputFilePath({ fileID: "file_demo", filename: "source" }, "", false)).toBe("/v1/files/file_demo/content");
    for (const id of [".", "..", "../foreign", "bad?token=secret", "bad#fragment", "", "a".repeat(257)]) expect(() => outputFilePath({ ...file, fileID: id }, "resp_demo", true)).toThrow("Invalid file ID");
    expect(() => outputFilePath(file, "resp_demo", false)).toThrow("stored Responses result");
    expect(() => outputFilePath(file, "../other", true)).toThrow();
    expect(() => outputFilePath({ ...file, containerID: "bad/other" }, "resp_demo", true)).toThrow();
    expect(outputFilename('../private\\report\n<name>.csv')).toBe("report__name_.csv");
    expect(outputFilename("/")).toBe("response-file");
  });
  it.each([
    ["image/png", png], ["IMAGE/JPEG; charset=binary", [255, 216, 255]], ["image/gif", [71, 73, 70, 56, 57, 97]], ["image/gif", [71, 73, 70, 56, 55, 97]], ["image/webp", [82, 73, 70, 70, 0, 0, 0, 0, 87, 69, 66, 80]],
  ])("checks %s MIME and raster signatures", async (type, bytes) => {
    expect(await checkedImageType(new Blob([Uint8Array.from(bytes)]), String(type))).toBe(String(type).split(";")[0].toLowerCase());
  });
  it.each(["image/svg+xml", "text/html", "application/pdf", "image/png", "image/jpeg", "image/gif", "image/webp"])("rejects active or mismatching %s previews", async (type) => {
    await expect(checkedImageType(new Blob(['<svg onload="alert(1)">']), type)).rejects.toThrow("verified PNG");
  });
  it("loads only on explicit action with the bounded authenticated transport and revokes previews", async () => {
    const conn = connection(), mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(png, { headers: { "Content-Type": "image/png" } }));
    const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:preview"), revoke = vi.spyOn(URL, "revokeObjectURL");
    const view = render(<FileResults files={[file]} responseID="resp_demo" stored connection={conn} />);
    expect(mock).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Preview report.png" }));
    expect(await screen.findByRole("img", { name: "report.png" })).toHaveAttribute("src", "blob:preview");
    expect(mock.mock.calls[0][0]).toBe("/test/v1/responses/resp_demo/containers/cntr_demo/files/cfile_demo/content");
    const options = mock.mock.calls[0][1]!; expect(options.method).toBe("GET"); expect(options.credentials).toBe("omit");
    expect(new Headers(options.headers).get("Authorization")).toBe("Bearer test-key"); expect(options.signal).toBeInstanceOf(AbortSignal);
    expect(create).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Close preview" }));
    expect(revoke).toHaveBeenCalledWith("blob:preview"); expect(screen.queryByRole("img")).not.toBeInTheDocument();
    mock.mockResolvedValue(new Response(png, { headers: { "Content-Type": "image/png" } }));
    await userEvent.click(screen.getByRole("button", { name: "Preview report.png" })); await screen.findByRole("img");
    view.rerender(<FileResults files={[file]} responseID="resp_demo" stored connection={conn} disabled />);
    expect(screen.queryByRole("img")).not.toBeInTheDocument(); expect(revoke).toHaveBeenCalledTimes(2);
  });
  it("downloads arbitrary files as attachments with sanitized names and immediate URL cleanup", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("<html>unsafe</html>", { headers: { "Content-Type": "text/html" } }));
    const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:download"), revoke = vi.spyOn(URL, "revokeObjectURL");
    let name = "", url = "";
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { name = this.download; url = this.href; });
    render(<FileResults files={[{ fileID: "file_demo", filename: "../../report.html" }]} responseID="" stored={false} connection={connection()} />);
    await userEvent.click(screen.getByRole("button", { name: "Download report.html" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Download started: report.html");
    expect(mock.mock.calls[0][0]).toBe("/test/v1/files/file_demo/content"); expect(name).toBe("report.html"); expect(url).toBe("blob:download");
    expect(create.mock.calls[0][0]).toHaveProperty("type", "application/octet-stream"); expect(revoke).toHaveBeenCalledWith("blob:download");
    expect(screen.queryByRole("link")).not.toBeInTheDocument(); expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });
  it.each(["disabled", "credential", "response", "file", "unmount", "cancel"])("discards late reads on %s changes even when the transport ignores abort", async (change) => {
    let resolve!: (response: Response) => void;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise((done) => { resolve = done; }));
    const create = vi.spyOn(URL, "createObjectURL"), conn = connection();
    const view = render(<FileResults files={[file]} responseID="resp_demo" stored connection={conn} />);
    await userEvent.click(screen.getByRole("button", { name: "Preview report.png" }));
    expect(screen.getByRole("button", { name: "Download report.png" })).toBeDisabled();
    const options = mock.mock.calls[0][1]!;
    if (change === "unmount") view.unmount();
    else if (change === "cancel") await userEvent.click(screen.getByRole("button", { name: "Cancel file request" }));
    else view.rerender(<FileResults files={[change === "file" ? { ...file, fileID: "cfile_other" } : file]} responseID={change === "response" ? "resp_other" : "resp_demo"} stored connection={change === "credential" ? connection() : conn} disabled={change === "disabled"} />);
    expect(options.signal?.aborted).toBe(true);
    await act(async () => { resolve(new Response(png, { headers: { "Content-Type": "image/png" } })); });
    expect(create).not.toHaveBeenCalled(); expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
  it("keeps errors local, rejects oversized reads, and permits an explicit retry", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"error":{"message":"Expired file"}}', { status: 404 }));
    const create = vi.spyOn(URL, "createObjectURL");
    render(<FileResults files={[file]} responseID="resp_demo" stored connection={connection()} />);
    await userEvent.click(screen.getByRole("button", { name: "Download report.png" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Expired file");
    mock.mockResolvedValue(new Response("large", { headers: { "Content-Length": String(32 * 1024 * 1024 + 1) } }));
    await userEvent.click(screen.getByRole("button", { name: "Download report.png" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Response exceeds"));
    mock.mockResolvedValue(new Response(png, { headers: { "Content-Type": "image/png", "Content-Length": String(8 * 1024 * 1024 + 1) } }));
    await userEvent.click(screen.getByRole("button", { name: "Preview report.png" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Response exceeds")); expect(create).not.toHaveBeenCalled();
  });
  it("never sends downloads without a connection or for known unstored generated files", () => {
    const mock = vi.spyOn(globalThis, "fetch");
    const view = render(<FileResults files={[file]} responseID="resp_demo" stored={false} connection={connection()} />);
    expect(screen.getByRole("button", { name: "Download report.png" })).toBeDisabled();
    view.rerender(<FileResults files={[file]} responseID="resp_demo" stored />);
    expect(screen.getByRole("button", { name: "Preview report.png" })).toBeDisabled(); expect(mock).not.toHaveBeenCalled();
  });
});
