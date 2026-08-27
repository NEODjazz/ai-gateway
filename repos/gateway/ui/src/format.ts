export function formatCost(cost: number, currency: string) {
  try {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: currency || "USD",
      minimumFractionDigits: 2,
      maximumFractionDigits: cost > 0 && cost < 0.01 ? 6 : 2
    }).format(cost);
  } catch {
    return `${currency || "USD"} ${cost.toFixed(cost > 0 && cost < 0.01 ? 6 : 2)}`;
  }
}

export function formatTimestamp(value: unknown) {
  if (typeof value !== "string" || !value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.valueOf())) return value;
  return parsed.toISOString().replace("T", " ").replace(/\.000Z$/, " UTC").replace(/Z$/, " UTC");
}
