//go:build doclingintegration

package documentprocessing

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestDoclingRealAPIAndWorker(t *testing.T) {
	address, key := os.Getenv("DOCLING_TEST_URL"), os.Getenv("DOCLING_TEST_API_KEY")
	if address == "" || key == "" {
		t.Fatal("Docling integration service configuration is required")
	}
	client, err := New(Config{URL: address, APIKey: key, Timeout: 240 * time.Second, PollInterval: 100 * time.Millisecond, MaxTextBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	text, err := client.Convert(t.Context(), openai.ResponseFileAttachment{Filename: "fixture.pdf", MediaType: "application/pdf", Data: base64.StdEncoding.EncodeToString(integrationPDF())}, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "GATEWAY PAGE ONE") || !strings.Contains(text, "GATEWAY PAGE TWO") {
		t.Fatal("incomplete generated PDF extraction")
	}
}

func integrationPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	for _, text := range []string{"GATEWAY PAGE ONE", "GATEWAY PAGE TWO"} {
		stream := "BT /F1 24 Tf 50 700 Td (" + text + ") Tj ET"
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}
