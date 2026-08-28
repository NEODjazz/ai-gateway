import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ColumnsMenu } from "./ColumnsMenu";
import { DataTable, displayValue, type Column, type Row } from "./DataTable";

function searchable(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

function compareValues(left: unknown, right: unknown): number {
  if (typeof left === "number" && typeof right === "number") return left - right;
  if (typeof left === "boolean" && typeof right === "boolean") return Number(left) - Number(right);
  return searchable(left).localeCompare(searchable(right), undefined, { numeric: true, sensitivity: "base" });
}

function RefreshIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>; }

export type ServerTableState = { search: string; onSearchChange: (value: string) => void; total: number; offset: number; pageSize: number; onPageSizeChange: (value: number) => void; onOffsetChange: (value: number) => void; sort: string; direction: "asc" | "desc"; onSortChange: (key: string, direction: "asc" | "desc") => void; sortableKeys: string[] };

export function ManagedDataTable({ rows, columns, actions, primaryAction, toolbarExtra, onRefresh, searchPlaceholder = "Search…", rowKey = "id", defaultHidden = [], server }: { rows: Row[]; columns: Column[]; actions?: (row: Row) => ReactNode; primaryAction?: ReactNode; toolbarExtra?: ReactNode; onRefresh?: () => void | Promise<void>; searchPlaceholder?: string; rowKey?: string; defaultHidden?: string[]; server?: ServerTableState }) {
  const [localSearch, setLocalSearch] = useState("");
  const [localSort, setLocalSort] = useState(columns[0]?.key || "");
  const [localDirection, setLocalDirection] = useState<"asc" | "desc">("asc");
  const [page, setPage] = useState(0);
  const [pageSizeOption, setPageSizeOption] = useState("25");
  const [customPageSize, setCustomPageSize] = useState("25");
  const [visible, setVisible] = useState(() => new Set(columns.filter((column) => !defaultHidden.includes(column.key)).map((column) => column.key)));
  const search = server?.search ?? localSearch;
  const sort = server?.sort ?? localSort;
  const direction = server?.direction ?? localDirection;
  const columnChoices = useMemo(() => columns.map((column, index) => ({ key: column.key, label: column.label, locked: index === 0 })), [columns]);
  const shownColumns = columns.filter((column) => visible.has(column.key)).map((column) => ({ ...column, label: column.key === sort ? `${column.label} ${direction === "asc" ? "↑" : "↓"}` : `${column.label} ↕` }));
  const filteredRows = useMemo(() => {
    if (server) return rows;
    const query = search.trim().toLowerCase();
    const filtered = query ? rows.filter((row) => columns.some((column) => searchable(row[column.key]).toLowerCase().includes(query))) : rows;
    return [...filtered].sort((left, right) => (direction === "asc" ? 1 : -1) * compareValues(left[sort], right[sort]));
  }, [columns, direction, rows, search, server, sort]);
  const localPageSize = pageSizeOption === "custom" ? Math.min(500, Math.max(1, Number(customPageSize) || 1)) : Number(pageSizeOption);
  const pageSize = server?.pageSize ?? localPageSize;
  const total = server?.total ?? filteredRows.length;
  const activePage = server ? Math.floor(server.offset / pageSize) : page;
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const pageRows = server ? rows : filteredRows.slice(page * pageSize, (page + 1) * pageSize);
  useEffect(() => { if (!server) setPage(0); }, [customPageSize, pageSizeOption, search, sort, direction, server]);
  useEffect(() => { if (!server && page >= pageCount) setPage(pageCount - 1); }, [page, pageCount, server]);
  function sortBy(key: string) {
    if (server) { if (!server.sortableKeys.includes(key)) return; server.onSortChange(key, sort === key && direction === "asc" ? "desc" : "asc"); return; }
    if (sort === key) setLocalDirection((value) => value === "asc" ? "desc" : "asc"); else { setLocalSort(key); setLocalDirection("asc"); }
  }
  function changePageSize(value: string) {
    setPageSizeOption(value);
    if (server && value !== "custom") server.onPageSizeChange(Number(value));
  }
  function changeCustomPageSize(value: string) {
    setCustomPageSize(value);
    if (server) server.onPageSizeChange(Math.min(200, Math.max(1, Number(value) || 1)));
  }
  function changePage(next: number) { if (server) server.onOffsetChange(next * pageSize); else setPage(next); }
  const sortableColumns = shownColumns.map((column) => ({ ...column, label: column.label.replace(/ [↑↓↕]$/, ""), render: column.render }));

  return <>
    <div className="key-toolbar">{primaryAction || <span />}<div className="key-toolbar-right"><label className="key-search"><span className="sr-only">{searchPlaceholder}</span><input aria-label={searchPlaceholder} placeholder={searchPlaceholder} value={search} onChange={(event) => server ? server.onSearchChange(event.target.value) : setLocalSearch(event.target.value)} /></label><ColumnsMenu columns={columnChoices} visible={visible} onChange={setVisible} />{toolbarExtra}{onRefresh && <button type="button" className="secondary icon-only-button" aria-label="Refresh table" title="Refresh" onClick={() => void onRefresh()}><RefreshIcon /></button>}</div></div>
    {pageRows.length ? <div className="table-card"><div className="table-scroll"><table><thead><tr>{shownColumns.map((column) => <th key={column.key}><button className="sort-button" onClick={() => sortBy(column.key)}>{column.label}</button></th>)}{actions && <th><span className="sr-only">Actions</span></th>}</tr></thead><tbody>{pageRows.map((row, index) => <tr key={String(row[rowKey] || row.id || row.name || index)}>{sortableColumns.map((column) => <td key={column.key}>{column.render ? column.render(row[column.key], row) : displayValue(row[column.key])}</td>)}{actions && <td className="row-actions">{actions(row)}</td>}</tr>)}</tbody></table></div></div> : <DataTable rows={[]} columns={shownColumns} />}
    <div className="key-pagination"><div className="key-pagination-summary"><label>Rows per page<select aria-label="Rows per page" value={server && ![10, 25, 50, 100].includes(pageSize) ? "custom" : pageSizeOption} onChange={(event) => changePageSize(event.target.value)}>{[10, 25, 50, 100].map((size) => <option value={size} key={size}>{size}</option>)}<option value="custom">Custom</option></select></label>{(server && ![10, 25, 50, 100].includes(pageSize) || pageSizeOption === "custom") && <label>Custom rows<input aria-label="Custom rows per page" type="number" min="1" max={server ? 200 : 500} value={server ? pageSize : customPageSize} onChange={(event) => changeCustomPageSize(event.target.value)} /></label>}<span>{total ? `${activePage * pageSize + 1}–${Math.min((activePage + 1) * pageSize, total)} of ${total}` : "0 results"}</span></div><div className="key-pagination-navigation"><button className="secondary" disabled={activePage === 0} onClick={() => changePage(Math.max(0, activePage - 1))}>Previous</button><button className="secondary" disabled={activePage + 1 >= pageCount} onClick={() => changePage(Math.min(pageCount - 1, activePage + 1))}>Next</button></div></div>
  </>;
}
