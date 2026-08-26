package gateway

import "net/http"

type CacheRuntimeConfig struct {
	ExactTTLSeconds    int `json:"exact_ttl_seconds"`
	ExactMaxBytes      int `json:"exact_max_bytes"`
	SemanticTTLSeconds int `json:"semantic_ttl_seconds"`
	SemanticMaxEntries int `json:"semantic_max_entries"`
	SemanticMaxBytes   int `json:"semantic_max_bytes"`
}

type CacheKindDiagnostics struct {
	Enabled  bool    `json:"enabled"`
	Hits     uint64  `json:"hits"`
	Misses   uint64  `json:"misses"`
	Errors   uint64  `json:"errors"`
	Writes   uint64  `json:"writes"`
	HitRatio float64 `json:"hit_ratio"`
}

type CacheDiagnostics struct {
	Config     CacheRuntimeConfig           `json:"config"`
	Exact      CacheKindDiagnostics         `json:"exact"`
	Semantic   CacheKindDiagnostics         `json:"semantic"`
	Operations map[string]map[string]uint64 `json:"operations"`
}

func (h Handler) WithCacheDiagnostics(config CacheRuntimeConfig) Handler {
	h.cacheConfig = config
	return h
}

func (h Handler) GetCacheDiagnostics(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "monitoring_unavailable", "cache diagnostics are unavailable")
		return
	}
	operations := h.metrics.CacheOperations()
	exact := cacheKindFromOperations(operations, "get", "set")
	exact.Enabled = h.cacheConfig.ExactTTLSeconds > 0
	semantic := cacheKindFromOperations(operations, "semantic_get", "semantic_set")
	semantic.Enabled = h.cacheConfig.SemanticTTLSeconds > 0
	writeJSON(w, http.StatusOK, CacheDiagnostics{Config: h.cacheConfig, Exact: exact, Semantic: semantic, Operations: operations})
}

func cacheKindFromOperations(operations map[string]map[string]uint64, getOperation, setOperation string) CacheKindDiagnostics {
	get := operations[getOperation]
	set := operations[setOperation]
	diagnostics := CacheKindDiagnostics{Hits: get["hit"], Misses: get["miss"], Errors: get["error"] + set["error"], Writes: set["ok"]}
	lookups := diagnostics.Hits + diagnostics.Misses
	if lookups > 0 {
		diagnostics.HitRatio = float64(diagnostics.Hits) / float64(lookups)
	}
	return diagnostics
}
