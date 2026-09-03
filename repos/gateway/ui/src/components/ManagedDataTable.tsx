import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Magnifier } from "@gravity-ui/icons";
import { Icon, Pagination, Select, Table, TextInput, withTableSelection, type TableColumnConfig } from "@gravity-ui/uikit";
import { ColumnsMenu } from "./ColumnsMenu";
import { DataTable, displayValue, type Column, type Row } from "./DataTable";
import { ToolbarIconButton } from "./ToolbarIconButton";
import { GravityThemeScope } from "./GravityThemeScope";

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

export type ServerTableState = { search: string; onSearchChange: (value: string) => void; total: number; offset: number; pageSize: number; onPageSizeChange: (value: number) => void; onOffsetChange: (value: number) => void; sort: string; direction: "asc" | "desc"; onSortChange: (key: string, direction: "asc" | "desc") => void; sortableKeys: string[] };
export type TableSelectionState = { selectedIds: string[]; onSelectionChange: (ids: string[]) => void; isRowSelectionDisabled?: (row: Row, index: number) => boolean };

const SelectableTable = withTableSelection<Row>(Table);

export function ManagedDataTable({ rows, columns, actions, primaryAction, toolbarExtra, onRefresh, searchPlaceholder = "Search…", rowKey = "id", defaultHidden = [], server, selection }: { rows: Row[]; columns: Column[]; actions?: (row: Row) => ReactNode; primaryAction?: ReactNode; toolbarExtra?: ReactNode; onRefresh?: () => void | Promise<void>; searchPlaceholder?: string; rowKey?: string; defaultHidden?: string[]; server?: ServerTableState; selection?: TableSelectionState }) {
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
  const tableColumns: TableColumnConfig<Row>[] = shownColumns.map((column, index) => ({
    id: column.key,
    name: () => <button className="sort-button" onClick={() => sortBy(column.key)}>{column.label}</button>,
    primary: index === 0,
    template: (row) => {
      const rawColumn = sortableColumns[index];
      return rawColumn.render ? rawColumn.render(row[rawColumn.key], row) : displayValue(row[rawColumn.key]);
    },
  }));
  if (actions) tableColumns.push({
    id: "_actions",
    name: () => <span className="sr-only">Actions</span>,
    align: "end",
    sticky: "end",
    width: 52,
    template: (row) => <div className="row-actions">{actions(row)}</div>,
  });

  return <>
    <div className="key-toolbar">{primaryAction || <span />}<div className="key-toolbar-right"><GravityThemeScope className="gravity-search-scope"><TextInput className="key-search" size="l" type="search" controlProps={{ "aria-label": searchPlaceholder }} placeholder={searchPlaceholder} value={search} startContent={<Icon data={Magnifier} size={16} />} onUpdate={(value) => server ? server.onSearchChange(value) : setLocalSearch(value)} /></GravityThemeScope><ColumnsMenu columns={columnChoices} visible={visible} onChange={setVisible} />{toolbarExtra}{onRefresh && <ToolbarIconButton icon="refresh" label="Refresh table" onClick={() => void onRefresh()} />}</div></div>
    {pageRows.length ? <GravityThemeScope className="gravity-table-scope"><div className="table-card">{selection ? <SelectableTable
      aria-label="Managed data table"
      className="gateway-table"
      columns={tableColumns}
      data={pageRows}
      edgePadding
      getRowId={(row, index) => String(row[rowKey] || row.id || row.name || index)}
      isRowSelectionDisabled={selection.isRowSelectionDisabled}
      onSelectionChange={selection.onSelectionChange}
      selectedIds={selection.selectedIds}
      verticalAlign="middle"
      width="max"
      wordWrap
    /> : <Table<Row>
      aria-label="Managed data table"
      className="gateway-table"
      columns={tableColumns}
      data={pageRows}
      edgePadding
      getRowId={(row, index) => String(row[rowKey] || row.id || row.name || index)}
      verticalAlign="middle"
      width="max"
      wordWrap
    />}</div></GravityThemeScope> : <DataTable rows={[]} columns={shownColumns} />}
    <div className="key-pagination"><div className="key-pagination-summary"><label><span>Rows per page</span><GravityThemeScope className="gravity-pagination-size"><Select aria-label="Rows per page" size="l" width={104} value={[server && ![10, 25, 50, 100].includes(pageSize) ? "custom" : pageSizeOption]} options={[10, 25, 50, 100].map((size) => ({ value: String(size), content: String(size) })).concat({ value: "custom", content: "Custom" })} onUpdate={(value) => changePageSize(value[0] || "25")} /></GravityThemeScope></label>{(server && ![10, 25, 50, 100].includes(pageSize) || pageSizeOption === "custom") && <label><span>Custom rows</span><GravityThemeScope className="gravity-pagination-custom"><TextInput aria-label="Custom rows per page" size="l" type="number" controlProps={{ min: 1, max: server ? 200 : 500 }} value={String(server ? pageSize : customPageSize)} onUpdate={changeCustomPageSize} /></GravityThemeScope></label>}<span>{total ? `${activePage * pageSize + 1}–${Math.min((activePage + 1) * pageSize, total)} of ${total}` : "0 results"}</span></div><GravityThemeScope className="gravity-pagination-navigation"><Pagination compact page={activePage + 1} pageSize={pageSize} total={total} onUpdate={(nextPage) => changePage(nextPage - 1)} /></GravityThemeScope></div>
  </>;
}
