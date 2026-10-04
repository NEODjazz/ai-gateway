// This public, server-rendered metadata mirrors the console's CSP connect-src.
// It contains exact origins only, never credentials or provider configuration.
export function playgroundOrigins(): string[] {
  const content = document.querySelector<HTMLMetaElement>('meta[name="ai-gateway-playground-origins"]')?.content;
  if (!content || content.length > 8192) return [];
  try {
    const values: unknown = JSON.parse(content);
    if (!Array.isArray(values) || values.length > 16) return [];
    const origins = values.map((value: unknown) => {
      if (typeof value !== "string") throw new Error("Invalid origin");
      const url = new URL(value);
      if (url.origin !== value || url.hostname.includes("*") || !["https:", "http:"].includes(url.protocol) || url.username || url.password || url.pathname !== "/" || url.search || url.hash) throw new Error("Invalid origin");
      if (url.protocol === "http:" && url.hostname !== "localhost" && url.hostname !== "[::1]" && !/^127(?:\.[0-9]{1,3}){3}$/.test(url.hostname)) throw new Error("Insecure origin");
      return url.origin;
    });
    return [...new Set(origins)];
  } catch { return []; }
}

export function assertPlaygroundOrigin(baseURL: string) {
  if (!baseURL) return;
  const target = new URL(baseURL);
  if (target.origin !== window.location.origin && !playgroundOrigins().includes(target.origin)) throw new Error("This gateway origin is not trusted by the console. An administrator must configure ADMIN_UI_PLAYGROUND_ORIGINS before a test key can be sent there.");
  if (window.location.protocol === "https:" && target.protocol === "http:") throw new Error("An HTTPS console requires an HTTPS gateway URL. The browser blocks insecure mixed-content connections.");
}
