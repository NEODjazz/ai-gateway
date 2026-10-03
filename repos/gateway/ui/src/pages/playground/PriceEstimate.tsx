import { TextControl } from "./Controls";

export type PricingInputs = { input: string; output: string; currency: string };
export const defaultPricing: PricingInputs = { input: "", output: "", currency: "USD" };
export function estimateCost(pricing: PricingInputs, input?: number, output?: number): string {
  if (!pricing.input.trim() || !pricing.output.trim() || input === undefined || output === undefined) return "Unavailable";
  const inputPrice = Number(pricing.input), outputPrice = Number(pricing.output), currency = pricing.currency.trim().toUpperCase();
  if (![inputPrice, outputPrice, input, output].every((value) => Number.isFinite(value) && value >= 0) || !/^[A-Z]{3}$/.test(currency)) return "Invalid pricing";
  const cost = (input * inputPrice + output * outputPrice) / 1_000_000;
  if (!Number.isFinite(cost)) return "Invalid pricing";
  return `${cost.toLocaleString("en-US", { minimumFractionDigits: 6, maximumFractionDigits: 8 })} ${currency}`;
}
export function PricingControls({ value, onUpdate, label = "", disabled }: { value: PricingInputs; onUpdate: (value: PricingInputs) => void; label?: string; disabled?: boolean }) {
  return <details className="playground-advanced"><summary>Cost estimate{label && ` ${label}`}</summary>
    <TextControl label={`Input price per million tokens${label}`} type="number" disabled={disabled} value={value.input} onUpdate={(input) => onUpdate({ ...value, input })} placeholder="Enter your applicable rate" />
    <TextControl label={`Output price per million tokens${label}`} type="number" disabled={disabled} value={value.output} onUpdate={(output) => onUpdate({ ...value, output })} placeholder="Enter your applicable rate" />
    <TextControl label={`Estimate currency${label}`} disabled={disabled} value={value.currency} onUpdate={(currency) => onUpdate({ ...value, currency })} />
    <p className="muted">Estimate uses your entered rates and reported input/output tokens. It excludes cache discounts, media, tool charges, taxes and final billing adjustments. Finalized charges appear in Usage &amp; spend.</p>
  </details>;
}
