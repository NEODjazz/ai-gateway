import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourceForm } from "./ResourceForm";

describe("ResourceForm", () => {
  it("converts csv, number, boolean and JSON values", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    render(<ResourceForm title="Edit resource" fields={[{ key: "tags", label: "Tags", type: "csv" }, { key: "limit", label: "Limit", type: "number" }, { key: "enabled", label: "Enabled", type: "boolean" }, { key: "metadata", label: "Metadata", type: "json" }]} onClose={() => {}} onSubmit={submit} />);
    expect(screen.getByLabelText("Tags")).toHaveClass("g-text-input__control");
    expect(screen.getByLabelText("Enabled")).toHaveClass("g-checkbox__control");
    expect(screen.getByLabelText("Metadata")).toHaveClass("g-text-area__control");
    await userEvent.type(screen.getByLabelText("Tags"), "one, two");
    await userEvent.clear(screen.getByLabelText("Limit")); await userEvent.type(screen.getByLabelText("Limit"), "7");
    await userEvent.click(screen.getByLabelText("Enabled"));
    fireEvent.change(screen.getByLabelText("Metadata"), { target: { value: '{"safe":true}' } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledWith({ tags: ["one", "two"], limit: 7, enabled: true, metadata: { safe: true } });
  });

  it("shows JSON parsing errors without submitting", async () => {
    const submit = vi.fn();
    render(<ResourceForm title="Edit resource" fields={[{ key: "metadata", label: "Metadata", type: "json" }]} onClose={() => {}} onSubmit={submit} />);
    fireEvent.change(screen.getByLabelText("Metadata"), { target: { value: "{" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
  });

  it("omits fields hidden by the current selector value", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    render(<ResourceForm title="Add provider" fields={[
      { key: "type", label: "Type", type: "select", options: ["azure-openai", "cohere"], defaultValue: "azure-openai" },
      { key: "api_version", label: "Azure API version", visibleWhen: { fieldKey: "type", equals: "azure-openai" } }
    ]} onClose={() => {}} onSubmit={submit} />);

    await userEvent.type(screen.getByLabelText("Azure API version"), "2025-04-01-preview");
    await userEvent.selectOptions(screen.getByLabelText("Type"), "cohere");
    expect(screen.queryByLabelText("Azure API version")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(submit).toHaveBeenCalledWith({ type: "cohere" });
  });

  it("restricts dependent select options and clears a stale selection", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    render(<ResourceForm title="Add provider" fields={[
      { key: "type", label: "Type", type: "select", options: ["azure-openai", "gemini"], defaultValue: "azure-openai" },
      { key: "auth_type", label: "Authentication", type: "select", optionsBy: { fieldKey: "type", values: { "azure-openai": ["api_key", "entra"], gemini: ["api_key", "gcp_adc"] } } }
    ]} onClose={() => {}} onSubmit={submit} />);

    expect(screen.getByRole("option", { name: "entra" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "gcp_adc" })).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Authentication"), "entra");
    await userEvent.selectOptions(screen.getByLabelText("Type"), "gemini");
    expect(screen.getByLabelText("Authentication")).toHaveValue("");
    expect(screen.queryByRole("option", { name: "entra" })).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: "gcp_adc" })).toBeInTheDocument();
  });

  it("selects multiple fixed values as removable chips", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    render(<ResourceForm title="Edit model" fields={[{
      key: "capabilities", label: "Capabilities", type: "multi-select",
      chipOptions: [{ value: "chat", label: "Chat" }, { value: "tools", label: "Tools" }, { value: "vision", label: "Vision" }]
    }]} initial={{ capabilities: ["chat"] }} onClose={() => {}} onSubmit={submit} />);

    expect(screen.getByRole("button", { name: "Remove capability chat" })).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Capabilities"));
    await userEvent.click(screen.getByRole("option", { name: /Tools/ }));
    expect(screen.getByRole("button", { name: "Remove capability tools" })).toBeInTheDocument();
    expect(screen.queryByText(/Custom exact or wildcard grant/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(submit).toHaveBeenCalledWith({ capabilities: ["chat", "tools"] });
  });

  it("loads configured references, filters dependent options and submits their IDs", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    const loadOptions = vi.fn(async (path: string) => {
      if (path === "/providers") return { data: [{ id: "azure", type: "openai-compatible" }, { id: "ollama", type: "ollama" }] };
      if (path === "/credentials") return { data: [{ id: "azure-key", provider_id: "azure", description: "Production" }, { id: "local-key", provider_id: "ollama" }] };
      return { models: [{ provider: "azure", model: "gpt-5" }, { provider: "ollama", model: "llama3" }] };
    });
    render(<ResourceForm title="Add deployment" fields={[
      { key: "provider_id", label: "Provider", type: "reference", reference: { path: "/providers", labelKeys: ["type"] } },
      { key: "credential_id", label: "Credential", type: "reference", reference: { path: "/credentials", labelKeys: ["description"], filter: { fieldKey: "provider_id", recordKey: "provider_id" } } },
      { key: "models", label: "Models", type: "reference-multi", reference: { path: "/models", collectionKey: "models", valueKey: "model", labelKeys: ["provider"] } }
    ]} loadOptions={loadOptions} onClose={() => {}} onSubmit={submit} />);

    expect(await screen.findByRole("option", { name: "azure — openai-compatible" })).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Provider"), "azure");
    expect(screen.getByRole("option", { name: "azure-key — Production" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "local-key" })).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Credential"), "azure-key");
    await userEvent.selectOptions(screen.getByLabelText("Provider"), "ollama");
    expect(screen.getByLabelText("Credential")).toHaveValue("");
    expect(screen.getByRole("option", { name: "local-key" })).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Provider"), "azure");
    await userEvent.selectOptions(screen.getByLabelText("Credential"), "azure-key");
    await userEvent.selectOptions(screen.getByLabelText("Models"), ["gpt-5", "llama3"]);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(loadOptions).toHaveBeenCalledTimes(3);
    expect(submit).toHaveBeenCalledWith({ provider_id: "azure", credential_id: "azure-key", models: ["gpt-5", "llama3"] });
  });

  it("switches a reference dropdown from another field and clears the previous selection", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    const loadOptions = vi.fn(async (path: string) => path === "/tags" ? { data: [{ name: "production", description: "Production traffic", enabled: true }] } : { data: [] });
    render(<ResourceForm title="Add budget" fields={[
      { key: "scope_type", label: "Scope type", type: "select", options: ["global", "tag"], defaultValue: "global" },
      { key: "scope_id", label: "Scope", type: "reference", referenceBy: { fieldKey: "scope_type", values: { global: { staticOptions: [{ value: "*", label: "All traffic (*)" }] }, tag: { path: "/tags", valueKey: "name", labelKeys: ["description"], enabledOnly: true } } } }
    ]} loadOptions={loadOptions} onClose={() => {}} onSubmit={submit} />);

    await userEvent.selectOptions(screen.getByLabelText("Scope"), "*");
    await userEvent.selectOptions(screen.getByLabelText("Scope type"), "tag");
    expect(screen.getByLabelText("Scope")).toHaveValue("");
    await userEvent.selectOptions(screen.getByLabelText("Scope"), await screen.findByRole("option", { name: "production — Production traffic" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(loadOptions).toHaveBeenCalledWith("/tags");
    expect(submit).toHaveBeenCalledWith({ scope_type: "tag", scope_id: "production" });
  });

  it("closes from the accessible close button", async () => {
    const close = vi.fn();
    render(<ResourceForm title="Edit resource" fields={[]} onClose={close} onSubmit={async () => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(close).toHaveBeenCalledOnce();
  });
});
