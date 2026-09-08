import { csvCell } from "./csv";
it.each([
  ['team "A", finance', '"team ""A"", finance"'],
  ["line1\nline2", '"line1\nline2"'],
  ["Москва", '"Москва"'],
  ["=1+1", '"\'=1+1"'],
  [" @SUM(A1)", '"\' @SUM(A1)"'],
  [-12, '"-12"'],
  [null, '""'],
])("exports safe CSV cell %s", (input, expected) => expect(csvCell(input)).toBe(expected));
