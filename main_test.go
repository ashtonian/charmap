package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	benchOpenDelim  = []byte("{{")
	benchCloseDelim = []byte("}}")
)

/*
makeTestBlob generates:

  - txt      – byte slice of (approx) size bytes
  - vals     – map used for replacement

Returns deterministic output for a given seed so benches are comparable.
*/
func makeTestBlob(size, keys int, seed int64) (txt []byte, vals map[string]string) {
	if keys <= 0 {
		panic("keys must be >0")
	}
	rng := rand.New(rand.NewSource(seed))

	// Build values map first.
	vals = make(map[string]string, keys)
	for i := 0; i < keys; i++ {
		k := "K" + strconv.Itoa(i)
		v := "V" + strconv.Itoa(i)
		vals[k] = v
	}

	var b bytes.Buffer
	keyList := make([]string, 0, keys)
	for k := range vals {
		keyList = append(keyList, k)
	}

	for b.Len() < size {
		if rng.Float64() < 0.08 { // 8 % chance emit a placeholder token
			k := keyList[rng.Intn(len(keyList))]
			b.Write(benchOpenDelim)
			b.WriteString(k)
			b.Write(benchCloseDelim)
		} else {
			b.WriteString(randomWord(rng))
		}
		b.WriteByte(' ')
	}
	return b.Bytes(), vals
}

func randomWord(rng *rand.Rand) string {
	n := rng.Intn(7) + 4
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteByte(byte('a' + rng.Intn(26)))
	}
	return sb.String()
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%dM", n>>20)
	default: // n < 1 MiB
		return fmt.Sprintf("%dK", n>>10)
	}
}

func BenchmarkReplacers(b *testing.B) {
	blobSizes := []int{4 << 10, 64 << 10, 512 << 10, 1024 << 10, 4096 << 10} // 4 KiB, 64 KiB, 512 KiB, 1 MiB, 4 MiB
	numKeys := []int{10, 100, 1000}

	for _, sz := range blobSizes {
		for _, k := range numKeys {
			blob, values := makeTestBlob(sz, k, 42)

			replacers := map[string]replacer{
				"regex": makeRegexReplacer(benchOpenDelim, benchCloseDelim, values),
				"strings.ReplaceAll": func(txt []byte) ([]byte, bool, error) {
					return stringsReplaceAllReplacer(txt, benchOpenDelim, benchCloseDelim, values)
				},
				"loop": func(txt []byte) ([]byte, bool, error) {
					return loopReplacer(txt, benchOpenDelim, benchCloseDelim, values)
				},
				"strings.Replacer": buildNewReplacer(benchOpenDelim, benchCloseDelim, values),
			}

			for name, fn := range replacers {
				label := fmt.Sprintf("%s/%d/%s", humanSize(sz), k, name)
				b.Run(label, func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						_, _, _ = fn(blob)
					}
				},
				)
			}
		}
	}
}

// regex
func makeRegexReplacer(open, close []byte, values map[string]string) replacer {
	openStr, closeStr := regexp.QuoteMeta(string(open)), regexp.QuoteMeta(string(close))
	re := regexp.MustCompile(openStr + `(.*?)` + closeStr) // safe for concurrent use

	return func(txt []byte) ([]byte, bool, error) {
		// (Optional) sanity-check that callers pass the same delimiters.
		if !bytes.Equal(open, open) || !bytes.Equal(close, close) {
			return nil, false, fmt.Errorf("makeRegexReplacer: mismatched delimiters")
		}

		changed := false
		var missingErr error

		out := re.ReplaceAllFunc(txt, func(m []byte) []byte {
			key := string(m[len(open) : len(m)-len(close)])
			val, ok := values[key]
			if !ok {
				missingErr = fmt.Errorf("env/flag %q not set", key)
				return m // leave token intact so caller sees original text if desired
			}
			changed = true
			return []byte(val)
		})

		if missingErr != nil {
			return nil, false, missingErr
		}
		return out, changed, nil
	}
}

// strings.ReplaceAll
func stringsReplaceAllReplacer(txt, open, close []byte, values map[string]string) ([]byte, bool, error) {
	s := string(txt)
	openStr := string(open)
	closeStr := string(close)
	changed := false

	for k, v := range values {
		token := openStr + k + closeStr
		if strings.Contains(s, token) {
			s = strings.ReplaceAll(s, token, v)
			changed = true
		}
	}

	if idx := strings.Index(s, openStr); idx != -1 {
		start := idx + len(openStr)
		if end := strings.Index(s[start:], closeStr); end != -1 {
			missing := s[start : start+end]
			return nil, false, fmt.Errorf("env/flag %q not set", missing)
		}
	}

	return []byte(s), changed, nil
}

func loopReplacer(txt, open, close []byte, values map[string]string) ([]byte, bool, error) {
	var out bytes.Buffer
	changed := false

	for i := 0; i < len(txt); {
		if bytes.HasPrefix(txt[i:], open) {
			start := i + len(open)
			end := bytes.Index(txt[start:], close)
			if end < 0 {
				out.Write(txt[i:])
				break
			}
			key := string(txt[start : start+end])
			val, ok := values[key]
			if !ok {
				return nil, false, fmt.Errorf("env/flag %q not set", key)
			}
			out.WriteString(val)
			i = start + end + len(close)
			changed = true
		} else {
			out.WriteByte(txt[i])
			i++
		}
	}

	return out.Bytes(), changed, nil
}

func TestProcessFiles_ReplacesKeys(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	srcFile := filepath.Join(tmp, "config.yaml")
	const input = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  domain: "<::PUBLIC_DOMAIN::>"
`
	if err := os.WriteFile(srcFile, []byte(input), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	ff, _ := newFileFilter([]string{`.*\.ya?ml$`}, nil)

	cfg := config{
		OpenDelim:  "<::",
		CloseDelim: "::>",
		TargetDir:  tmp,
		Workers:    1,
		KeyMap:     map[string]string{"PUBLIC_DOMAIN": "example.com"},
		FileFilter: ff,
		CloseLog:   func() {},
	}

	if err := processFiles(cfg); err != nil {
		t.Fatalf("processFiles returned error: %v", err)
	}

	got, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatalf("read back file: %v", err)
	}
	if !strings.Contains(string(got), "example.com") {
		t.Errorf("placeholder not replaced; file contents:\n%s", got)
	}
	if strings.Contains(string(got), "<::PUBLIC_DOMAIN::>") {
		t.Errorf("placeholder marker still present")
	}
}

func TestDefaultFileFilter(t *testing.T) {
	inc := sliceFlag{`.*\.ya?ml$`}
	ign := sliceFlag{`(^|/)\.git(/|$)`}

	ff, err := newFileFilter([]string(inc), []string(ign))
	if err != nil {
		t.Fatalf("newFileFilter: %v", err)
	}

	tests := []struct {
		path string
		want bool
	}{
		// should process
		{"config.yaml", true},
		{"values.yml", true},

		// wrong extension
		{"notes.txt", false},

		// ignored directory
		{".git/config", false},
		{filepath.Join("src", ".git", "index"), false},
	}

	for _, tt := range tests {
		got := ff.match(tt.path)
		if got != tt.want {
			t.Errorf("match(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func BenchmarkProcessFiles(b *testing.B) {
	const (
		filesPerRun = 100
		keyCount    = 32
	)

	// sizes in bytes
	cases := []struct {
		name string
		size int
	}{
		{"4K", 4 * 1024},
		{"64K", 64 * 1024},
		{"1M", 1 * 1024 * 1024},
	}

	for _, tc := range cases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				tmpDir := b.TempDir()
				blob, values := makeTestBlob(tc.size, keyCount, time.Now().UnixNano())

				for f := 0; f < filesPerRun; f++ {
					p := filepath.Join(tmpDir, "file"+strconv.Itoa(f)+".yaml")
					if err := os.WriteFile(p, blob, 0o644); err != nil {
						b.Fatalf("write temp file: %v", err)
					}
				}

				ff, err := newFileFilter([]string{`.*\.ya?ml$`}, []string{`^\.git(/|$)`})
				if err != nil {
					b.Fatalf("newFileFilter: %v", err)
				}

				cfg := config{
					OpenDelim:  string(benchOpenDelim),
					CloseDelim: string(benchCloseDelim),
					TargetDir:  tmpDir,
					Workers:    runtime.GOMAXPROCS(0),
					KeyMap:     values,
					FileFilter: ff,
					CloseLog:   func() {},
				}

				b.ResetTimer()
				if err := processFiles(cfg); err != nil {
					b.Fatalf("processFiles: %v", err)
				}
				b.StopTimer()
			}
		})
	}
}
