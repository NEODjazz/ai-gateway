package openai

type ContainerExpiresAfter struct {
	Anchor  string `json:"anchor"`
	Minutes int    `json:"minutes"`
}

type ContainerCreateRequest struct {
	Provider     string                 `json:"provider,omitempty"`
	Model        string                 `json:"model"`
	Name         string                 `json:"name"`
	ExpiresAfter *ContainerExpiresAfter `json:"expires_after,omitempty"`
	FileIDs      []string               `json:"file_ids,omitempty"`
	MemoryLimit  string                 `json:"memory_limit,omitempty"`
}

type ContainerProviderCreateRequest struct {
	Name         string                 `json:"name"`
	ExpiresAfter *ContainerExpiresAfter `json:"expires_after,omitempty"`
	MemoryLimit  string                 `json:"memory_limit,omitempty"`
}

type Container struct {
	ID           string                 `json:"id"`
	Object       string                 `json:"object"`
	CreatedAt    int64                  `json:"created_at"`
	Status       string                 `json:"status"`
	ExpiresAfter *ContainerExpiresAfter `json:"expires_after,omitempty"`
	LastActiveAt int64                  `json:"last_active_at"`
	MemoryLimit  string                 `json:"memory_limit"`
	Name         string                 `json:"name"`
}

type ContainerList struct {
	Object  string      `json:"object"`
	Data    []Container `json:"data"`
	FirstID string      `json:"first_id,omitempty"`
	LastID  string      `json:"last_id,omitempty"`
	HasMore bool        `json:"has_more"`
}

type ContainerDeletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}
