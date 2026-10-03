package vectorstate

import "testing"

func TestChunkingStrategyValidation(t *testing.T) {
	tests := []struct {
		name     string
		strategy ChunkingStrategy
		valid    bool
	}{
		{name: "auto", strategy: AutoChunkingStrategy(), valid: true},
		{name: "auto with settings", strategy: ChunkingStrategy{Type: "auto", MaxChunkSizeTokens: 100}, valid: false},
		{name: "static minimum", strategy: ChunkingStrategy{Type: "static", MaxChunkSizeTokens: 100, ChunkOverlapTokens: 50}, valid: true},
		{name: "static maximum", strategy: ChunkingStrategy{Type: "static", MaxChunkSizeTokens: 4096, ChunkOverlapTokens: 2048}, valid: true},
		{name: "static size below minimum", strategy: ChunkingStrategy{Type: "static", MaxChunkSizeTokens: 99}, valid: false},
		{name: "static overlap above half", strategy: ChunkingStrategy{Type: "static", MaxChunkSizeTokens: 800, ChunkOverlapTokens: 401}, valid: false},
		{name: "unset", strategy: ChunkingStrategy{}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.strategy.Valid(); got != test.valid {
				t.Fatalf("Valid()=%t want=%t strategy=%+v", got, test.valid, test.strategy)
			}
		})
	}
}
