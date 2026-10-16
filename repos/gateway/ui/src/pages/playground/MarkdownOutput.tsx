import { memo, useState } from "react";
import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { GatewayButton } from "../../components/GatewayButton";

function markdownURL(value: string): string | undefined {
  if (/^#[\w-]+$/.test(value)) return value;
  if (/^(https?:\/\/|mailto:)/i.test(value) && !/[\u0000-\u0020\u007f]/.test(value)) return value;
  return undefined;
}

function CodeBlock({ text, language }: { text: string; language?: string }) {
  const [result, setResult] = useState({ text: "", status: "" });
  const status = result.text === text ? result.status : "";
  return <div className="playground-code-block">
    <div className="playground-code-heading"><span>{language || "Code"}</span><GatewayButton type="button" view="flat" aria-label="Copy code" onClick={async () => {
      try { await navigator.clipboard.writeText(text); setResult({ text, status: "Copied" }); }
      catch { setResult({ text, status: "Could not copy code. Select the text to copy it." }); }
    }}>Copy code</GatewayButton>{status && <span role="status">{status}</span>}</div>
    <pre tabIndex={0} aria-label={language ? `${language} code` : "Code block"}><code>{text}</code></pre>
  </div>;
}

const components: Components = {
  pre: ({ node, children }) => {
    const code = node?.children[0];
    if (code?.type !== "element" || code.tagName !== "code") return <pre>{children}</pre>;
    const text = code.children.map((child) => child.type === "text" ? child.value : "").join("");
    const classes = code.properties.className;
    const language = Array.isArray(classes) ? classes.find((item) => String(item).startsWith("language-")) : undefined;
    return <CodeBlock text={text} language={language ? String(language).slice(9, 73) : undefined} />;
  },
  a: ({ href, children }) => href ? <a href={href} target={href.startsWith("#") ? undefined : "_blank"} rel="noopener noreferrer" referrerPolicy="no-referrer">{children}</a> : <span>{children}</span>,
  // Model-generated images must not silently contact external tracking URLs.
  img: ({ alt }) => <span className="playground-markdown-image">[Image: {alt || "image"}]</span>,
  table: ({ children }) => <div className="playground-markdown-table"><table>{children}</table></div>,
};

export const MarkdownOutput = memo(function MarkdownOutput({ text }: { text: string }) {
  return <div className="playground-markdown"><Markdown remarkPlugins={[remarkGfm]} components={components} urlTransform={markdownURL}>{text || "No text output"}</Markdown></div>;
});
