import { Card } from "@gravity-ui/uikit";
import { GravityThemeScope } from "./GravityThemeScope";

export function StatCard({ label, value, detail }: { label: string; value: string | number; detail?: string }) {
  return <article className="gravity-card-article"><GravityThemeScope className="gravity-card-scope"><Card type="container" view="raised" className="stat-card"><span>{label}</span><strong>{value}</strong>{detail && <small>{detail}</small>}</Card></GravityThemeScope></article>;
}
