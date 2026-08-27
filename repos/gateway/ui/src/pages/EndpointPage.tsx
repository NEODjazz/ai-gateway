import { useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";

export function EndpointPage({ eyebrow, title, description, path }: { eyebrow: string; title: string; description: string; path: string }) {
  const { client } = useAuth();
  const [payload, setPayload] = useState<unknown>();
  const [error, setError] = useState("");
  useEffect(() => { client.request(path).then(setPayload).catch((cause) => setError(cause instanceof Error ? cause.message : "Could not load data")); }, [client, path]);
  return <><PageHeader eyebrow={eyebrow} title={title} description={description} />{error ? <ErrorState message={error} /> : payload === undefined ? <LoadingState /> : <section className="json-card"><pre>{JSON.stringify(payload, null, 2)}</pre></section>}</>;
}
