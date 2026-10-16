package gateway

import (
	"testing"

	"ai-gateway-gateway/internal/vectorstate"
)

func TestPublicVectorStoreFileOptionalFilename(t *testing.T) {
	for _, filename := range []string{"", "guide.txt", "Руководство.txt"} {
		t.Run(filename, func(t *testing.T) {
			value := publicVectorStoreFile(vectorstate.File{
				VectorStoreID: "vs_owned", FileID: "file_owned", Filename: filename,
				OwnerKey: "private-owner", ChunkingStrategy: vectorstate.AutoChunkingStrategy(),
			})
			got, present := value["filename"]
			if present != (filename != "") || present && got != filename {
				t.Fatalf("filename=%v present=%v", got, present)
			}
			if _, exposed := value["owner_key"]; exposed {
				t.Fatal("private owner exposed")
			}
		})
	}
}
