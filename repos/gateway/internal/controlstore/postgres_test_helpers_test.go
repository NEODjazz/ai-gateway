package controlstore

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresDSNWithSearchPath(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "URL", dsn: "postgres://gateway:secret@localhost:5432/gateway?sslmode=disable"},
		{name: "keyword", dsn: "host=localhost port=5432 dbname=gateway user=gateway password=secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const schema = "gateway_test_0123456789abcdef"
			isolated, err := postgresDSNWithSearchPath(test.dsn, schema)
			if err != nil {
				t.Fatal(err)
			}
			config, err := pgxpool.ParseConfig(isolated)
			if err != nil {
				t.Fatal(err)
			}
			if got := config.ConnConfig.RuntimeParams["search_path"]; got != schema {
				t.Fatalf("search_path=%q, want %q", got, schema)
			}
		})
	}
}

func TestPostgresDSNWithSearchPathRejectsUnsafeKeywordSchema(t *testing.T) {
	if _, err := postgresDSNWithSearchPath("host=localhost", "unsafe schema"); err == nil {
		t.Fatal("expected unsafe schema to be rejected")
	}
}
