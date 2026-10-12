package token

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// corpus returns about size bytes of words with roughly one token per twelve
// words, drawn from keys keys. The same arguments always give the same bytes.
func corpus(size, keys int, withTokens bool) ([]byte, map[string]string) {
	rng := rand.New(rand.NewPCG(1, uint64(size)))
	values := make(map[string]string, keys)
	for i := range keys {
		values["KEY_"+strconv.Itoa(i)] = "value-" + strconv.Itoa(i)
	}
	var b strings.Builder
	for b.Len() < size {
		if withTokens && rng.IntN(12) == 0 {
			fmt.Fprintf(&b, "<::KEY_%d::> ", rng.IntN(keys))
			continue
		}
		for range 4 + rng.IntN(6) {
			b.WriteByte(byte('a' + rng.IntN(26)))
		}
		b.WriteByte(' ')
	}
	return []byte(b.String()), values
}

func BenchmarkRender(b *testing.B) {
	for _, size := range []int{4 << 10, 4 << 20} {
		for _, withTokens := range []bool{true, false} {
			src, values := corpus(size, 100, withTokens)
			e := newEngine(b, Config{Values: values})
			b.Run(fmt.Sprintf("size=%d/tokens=%t", size, withTokens), func(b *testing.B) {
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				for b.Loop() {
					if res := e.Render(src); len(res.Problems) > 0 {
						b.Fatal(res.Problems[0])
					}
				}
			})
		}
	}
}
