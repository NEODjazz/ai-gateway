import type { ReactNode } from "react";
import { EmptyState } from "./AsyncState";

export type Row = Record<string, unknown>;
export type Column = { key: string; label: string; render?: (value: unknown, row: Row) => ReactNode };

function displayValue(value: unknown): ReactNode {
  if (value === null || value === undefined || value === "") return <span className="muted">—</span>;
  if (typeof value === "boolean") return <span className={`status ${value ? "enabled" : "disabled"}`}>{value ? "Enabled" : "Disabled"}</span>;
  if (Array.isArray(value)) return <div className="tag-list">{value.map((item, index) => <span className="tag" key={`${String(item)}-${index}`}>{String(item)}</span>)}</div>;
  if (typeof value === "object") return <code>{JSON.stringify(value)}</code>;
  return String(value);
}

export function DataTable({ rows, columns, actions }: { rows: Row[]; columns: Column[]; actions?: (row: Row) => ReactNode }) {
  if (!rows.length) return <EmptyState>No records found.</EmptyState>;
  return <div className="table-card"><div className="table-scroll"><table><thead><tr>{columns.map((column) => <th key={column.key}>{column.label}</th>)}{actions && <th><span className="sr-only">Actions</span></th>}</tr></thead><tbody>{rows.map((row, index) => <tr key={String(row.id || row.name || index)}>{columns.map((column) => <td key={column.key}>{column.render ? column.render(row[column.key], row) : displayValue(row[column.key])}</td>)}{actions && <td className="row-actions">{actions(row)}</td>}</tr>)}</tbody></table></div></div>;
}
