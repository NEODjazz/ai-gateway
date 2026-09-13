package openai

type GeminiCachedContentUsage struct {
	TotalTokenCount int `json:"totalTokenCount"`
}

// GeminiCachedContent contains provider metadata only. Cached prompt content is
// input-only and is never persisted or returned by the gateway.
type GeminiCachedContent struct {
	Name          string                    `json:"name"`
	DisplayName   string                    `json:"displayName,omitempty"`
	Model         string                    `json:"model"`
	CreateTime    string                    `json:"createTime"`
	UpdateTime    string                    `json:"updateTime"`
	ExpireTime    string                    `json:"expireTime"`
	UsageMetadata *GeminiCachedContentUsage `json:"usageMetadata,omitempty"`
}

type GeminiCachedContentExpiration struct {
	TTL        string `json:"ttl,omitempty"`
	ExpireTime string `json:"expireTime,omitempty"`
}
