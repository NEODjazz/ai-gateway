import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { OverviewPage } from "./OverviewPage";

const json = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status });
it("uses the server total and keeps healthy inventory visible during a partial outage", async () => {
  let failed = true;
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    if (String(input).endsWith("/keys")) return json({ data: Array.from({ length: 100 }, (_, id) => ({ id })), total: 250 });
    if (String(input).endsWith("/providers") && failed) return json({ error: { message: "Providers unavailable" } }, 503);
    return json({ data: [{ id: "one" }] });
  });
  render(<AuthProvider><OverviewPage /></AuthProvider>);
  expect(await screen.findByText("250")).toBeInTheDocument();
  expect(await screen.findByText("Providers unavailable")).toBeInTheDocument();
  expect(screen.getByText("Deployments")).toBeInTheDocument();
  const keyRequests = () => fetchMock.mock.calls.filter(([path]) => String(path).endsWith("/keys")).length;
  expect(keyRequests()).toBe(1);
  failed = false;
  await userEvent.click(within(screen.getByRole("region", { name: "Providers" })).getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("Providers")).toBeInTheDocument();
  expect(screen.queryByText("Providers unavailable")).not.toBeInTheDocument();
  expect(keyRequests()).toBe(1);
});
