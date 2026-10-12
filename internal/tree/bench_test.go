package tree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/charmap/v2/internal/token"
)

// BenchmarkCheck measures selecting, reading and rendering a tree. Check is
// used so every iteration sees the same unmodified files.
func BenchmarkCheck(b *testing.B) {
	for _, files := range []int{50, 5000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := b.TempDir()
			content := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n  labels:\n    group: <::GROUP::>\n")
			for i := range files {
				p := filepath.Join(dir, fmt.Sprintf("d%02d", i%32), fmt.Sprintf("f%05d.yaml", i))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(p, content, 0o644); err != nil {
					b.Fatal(err)
				}
			}
			e, err := token.New(token.Config{Syntax: token.DefaultSyntax, Values: map[string]string{"GROUP": "us-east"}})
			if err != nil {
				b.Fatal(err)
			}
			opts := Options{Include: yaml, Workers: 8, MaxFileSize: 1 << 20}
			b.ReportAllocs()
			for b.Loop() {
				report, err := Check(context.Background(), dir, e, opts)
				if err != nil || len(report.Changed) != files {
					b.Fatalf("err=%v changed=%d", err, len(report.Changed))
				}
			}
		})
	}
}
