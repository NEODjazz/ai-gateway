// Keep reviewed native tool argument objects as their original JSON text.
// The marker is private and cannot be supplied by a JSON response.
const originalJSON = Symbol("native tool argument JSON");
type ExactObject = Record<string, unknown> & { [originalJSON]?: string };
const object = (value: unknown): value is ExactObject => !!value && typeof value === "object" && !Array.isArray(value);

export function parseExactObject(source: string): unknown {
  const value: unknown = JSON.parse(source);
  if (object(value)) Object.defineProperty(value, originalJSON, { value: source });
  return value;
}

export function exactObjectText(value: unknown): string {
  const raw = object(value) ? value[originalJSON] : undefined;
  if (raw !== undefined) {
    if (JSON.stringify(value) !== JSON.stringify(JSON.parse(raw))) throw new Error("Reviewed tool arguments changed. Clear the conversation before retrying.");
    return raw;
  }
  return JSON.stringify(value);
}

export function stringifyExactJSON(value: unknown): string {
  const active = new Set<object>();
  function encode(item: unknown, depth: number): string | undefined {
    if (depth > 128) throw new Error("Native JSON exceeds the 128-level depth limit.");
    if (item === null || typeof item !== "object") return JSON.stringify(item);
    if (object(item) && item[originalJSON] !== undefined) return exactObjectText(item);
    if (active.has(item)) throw new Error("Native JSON contains a circular reference.");
    active.add(item);
    try {
      if (Array.isArray(item)) return `[${Array.from(item, (entry) => encode(entry, depth + 1) ?? "null").join(",")}]`;
      return `{${Object.keys(item).flatMap((key) => {
        const encoded = encode((item as Record<string, unknown>)[key], depth + 1);
        return encoded === undefined ? [] : [`${JSON.stringify(key)}:${encoded}`];
      }).join(",")}}`;
    } finally { active.delete(item); }
  }
  const result = encode(value, 0);
  if (result === undefined) throw new Error("Native request must contain JSON.");
  return result;
}

// JSON.parse validates syntax first. This bounded lexical walk finds original
// input/arguments objects without interpreting their numeric literals again.
export function parseNativeJSON(source: string): unknown {
  if (new TextEncoder().encode(source).length > 2 * 1024 * 1024) throw new Error("Native output exceeds the 2 MiB Playground limit.");
  const parsed: unknown = JSON.parse(source);
  let offset = 0;
  const space = () => { while (/\s/.test(source[offset] || "")) offset++; };
  function stringEnd() {
    offset++;
    while (source[offset] !== '"') offset += source[offset] === "\\" ? 2 : 1;
    offset++;
  }
  function visit(value: unknown, depth: number) {
    if (depth > 128) throw new Error("Native JSON exceeds the 128-level depth limit.");
    space();
    if (source[offset] === '"') { stringEnd(); return; }
    if (source[offset] === "[") {
      offset++; space(); let index = 0;
      while (source[offset] !== "]") {
        visit(Array.isArray(value) ? value[index++] : undefined, depth + 1); space();
        if (source[offset] === ",") { offset++; space(); }
      }
      offset++; return;
    }
    if (source[offset] === "{") {
      offset++; space();
      while (source[offset] !== "}") {
        const start = offset; stringEnd(); const key: string = JSON.parse(source.slice(start, offset));
        space(); offset++; space(); const valueStart = offset;
        const child = object(value) ? value[key] : undefined;
        visit(child, depth + 1);
        if (object(value) && object(child) && (value.type === "tool_use" && key === "input" || value.type === "function_call" && key === "arguments")) {
          Object.defineProperty(child, originalJSON, { value: source.slice(valueStart, offset), configurable: true });
        }
        space(); if (source[offset] === ",") { offset++; space(); }
      }
      offset++; return;
    }
    while (offset < source.length && !/[\s,\]}]/.test(source[offset])) offset++;
  }
  visit(parsed, 0);
  return parsed;
}
