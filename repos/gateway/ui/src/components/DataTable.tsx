import type { ReactNode } from "react";
import { Table, type TableColumnConfig } from "@gravity-ui/uikit";
import { EmptyState } from "./AsyncState";
import { GravityThemeScope } from "./GravityThemeScope";

export type Row = Record<string, unknown>;
export type Column = { key: string; label: string; render?: (value: unknown, row: Row) => ReactNode };

export function displayValue(value: unknown): ReactNode {
  if (value === null || value === undefined || value === "") return <span className="muted">—</span>;
  if (typeof value === "boolean") return <span className={`status ${value ? "enabled" : "disabled"}`}>{value ? "Enabled" : "Disabled"}</span>;
  if (Array.isArray(value)) return <div className="tag-list">{value.map((item, index) => <span className="tag" key={`${String(item)}-${index}`}>{String(item)}</span>)}</div>;
  if (typeof value === "object") return <code>{JSON.stringify(value)}</code>;
  return String(value);
}

export function DataTable({ rows, columns, actions }: { rows: Row[]; columns: Column[]; actions?: (row: Row) => ReactNode }) {
  if (!rows.length) return <EmptyState>No records found.</EmptyState>;
  const tableColumns: TableColumnConfig<Row>[] = columns.map((column) => ({
    id: column.key,
    name: column.label,
    template: (row) => column.render ? column.render(row[column.key], row) : displayValue(row[column.key]),
  }));
  if (actions) tableColumns.push({
    id: "_actions",
    name: () => <span className="sr-only">Actions</span>,
    align: "end",
    sticky: "end",
    width: 52,
    template: (row) => <div className="row-actions">{actions(row)}</div>,
  });
  return <GravityThemeScope className="gravity-table-scope"><div className="table-card"><Table<Row>
    aria-label="Data table"
    className="gateway-table"
    columns={tableColumns}
    data={rows}
    edgePadding
    getRowId={(row, index) => String(row.id || row.name || index)}
    verticalAlign="middle"
    width="max"
    wordWrap
  /></div></GravityThemeScope>;
}
