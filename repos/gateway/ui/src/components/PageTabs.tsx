import { Tab, TabList } from "@gravity-ui/uikit";
import { GravityThemeScope } from "./GravityThemeScope";

export type PageTab<Value extends string> = { value: Value; label: string };

export function PageTabs<Value extends string>({ label, value, items, onUpdate, className = "" }: { label: string; value: Value; items: PageTab<Value>[]; onUpdate: (value: Value) => void; className?: string }) {
  return <GravityThemeScope className={`gravity-page-tabs${className ? ` ${className}` : ""}`}><TabList aria-label={label} contentOverflow="scroll" size="l" value={value} onUpdate={(next) => onUpdate(next as Value)}>{items.map((item) => <Tab key={item.value} value={item.value}>{item.label}</Tab>)}</TabList></GravityThemeScope>;
}
