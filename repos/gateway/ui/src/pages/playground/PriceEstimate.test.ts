import { defaultPricing, estimateCost } from "./PriceEstimate";
describe("Playground estimated token cost", () => {
  it("uses explicitly supplied rates and labels their currency", () => {
    expect(estimateCost({ input: "2", output: "8", currency: "usd" }, 1000, 500)).toBe("0.006000 USD");
    expect(estimateCost({ input: "0", output: "0", currency: "EUR" }, 0, 0)).toBe("0.000000 EUR");
  });
  it("does not substitute zero for missing rates or missing usage", () => {
    expect(estimateCost(defaultPricing, 1000, 500)).toBe("Unavailable");
    expect(estimateCost({ input: "2", output: "8", currency: "USD" }, 0, undefined)).toBe("Unavailable");
  });
  it.each([{ input: "-1" }, { output: "Infinity" }, { currency: "dollars" }])("rejects invalid pricing %j", (patch) => {
    expect(estimateCost({ input: "2", output: "8", currency: "USD", ...patch }, 100, 100)).toBe("Invalid pricing");
  });
});
