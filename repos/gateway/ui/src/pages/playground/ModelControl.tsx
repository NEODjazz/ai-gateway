import { useEffect, useState, type ReactNode } from "react";
import { Select, TextInput } from "@gravity-ui/uikit";
import { GatewayButton } from "../../components/GatewayButton";
import { GravityThemeScope } from "../../components/GravityThemeScope";

type Props = { label: string; value: string; models: string[]; onUpdate: (value: string) => void; disabled?: boolean; loading?: boolean; scope: unknown; refresh?: ReactNode };

export function ModelControl({ label, value, models, onUpdate, disabled = false, loading = false, scope, refresh }: Props) {
  const [manual, setManual] = useState(false);
  useEffect(() => { setManual(false); }, [scope]);
  const input = manual || !models.length || !!value && !models.includes(value);
  const locked = disabled || loading;
  return <div className="playground-model-picker"><label>{label}<div className={refresh ? "playground-model-control" : undefined}>
    <GravityThemeScope className="gravity-playground-control">{input
      ? <TextInput aria-label={label} size="l" disabled={locked} value={value} onUpdate={onUpdate} placeholder="Enter a model ID" />
      : <Select aria-label={label} size="l" width="max" filterable disabled={locked} loading={loading} value={value ? [value] : []} options={models.map((id) => ({ value: id, content: id }))} onUpdate={([id]) => onUpdate(id || "")} placeholder="Select a model" />}
    </GravityThemeScope>{refresh}</div></label>
    {models.length > 0 && <GatewayButton view="flat" disabled={locked} aria-label={`${label}: ${input ? "choose from catalog" : "enter ID manually"}`} onClick={() => {
      setManual(!input);
      if (input && !models.includes(value)) onUpdate(models[0]);
    }}>{input ? "Choose from catalog" : "Enter model ID"}</GatewayButton>}
  </div>;
}
