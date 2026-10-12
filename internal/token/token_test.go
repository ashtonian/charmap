package token

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEngine(t testing.TB, cfg Config) *Engine {
	t.Helper()
	if cfg.Syntax == (Syntax{}) {
		cfg.Syntax = DefaultSyntax
	}
	e, err := New(cfg)
	require.NoError(t, err)
	return e
}

func TestRender(t *testing.T) {
	values := map[string]string{
		"GROUP":   "us-east",
		"CLUSTER": "cluster-001",
		"EMPTY":   "",
		"LITERAL": "<::NOT_A_KEY::>",
		"A":       "short",
		"AB":      "long",
	}
	tests := []struct {
		name     string
		in       string
		want     string
		replaced int
	}{
		{name: "no tokens", in: "plain: text\n", want: "plain: text\n"},
		{name: "one token", in: "group: <::GROUP::>", want: "group: us-east", replaced: 1},
		{name: "adjacent tokens", in: "<::GROUP::><::CLUSTER::>", want: "us-eastcluster-001", replaced: 2},
		{name: "empty value", in: "a<::EMPTY::>b", want: "ab", replaced: 1},
		{name: "value containing delimiters is not rescanned", in: "x: <::LITERAL::>", want: "x: <::NOT_A_KEY::>", replaced: 1},
		{name: "prefix-sharing keys", in: "<::A::> <::AB::>", want: "short long", replaced: 2},
		{name: "crlf and bom preserved", in: "\xef\xbb\xbfa: <::GROUP::>\r\nb: 1\r\n", want: "\xef\xbb\xbfa: us-east\r\nb: 1\r\n", replaced: 1},
		{name: "invalid utf-8 preserved", in: "\xff<::GROUP::>\x00", want: "\xffus-east\x00", replaced: 1},
		{name: "close delimiter alone is text", in: "a ::> b", want: "a ::> b"},
		{name: "other template languages pass through", in: "{{ .Values.x }} ${{ github.ref }}", want: "{{ .Values.x }} ${{ github.ref }}"},
	}
	e := newEngine(t, Config{Values: values})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := e.Render([]byte(tt.in))
			require.Empty(t, res.Problems)
			assert.Equal(t, tt.want, string(res.Output))
			assert.Equal(t, tt.replaced, res.Replaced)
		})
	}
}

func TestRenderWithoutTokensReturnsInput(t *testing.T) {
	e := newEngine(t, Config{Values: map[string]string{"A": "x"}})
	src := []byte("no tokens here")
	res := e.Render(src)
	require.Empty(t, res.Problems)
	assert.Same(t, &src[0], &res.Output[0])
}

func TestRenderProblems(t *testing.T) {
	e := newEngine(t, Config{Values: map[string]string{"A": "x"}})
	tests := []struct {
		name string
		in   string
		want []Problem
	}{
		{
			name: "every missing key is reported with its position",
			in:   "<::A::> <::B::>\n  <::C::>",
			want: []Problem{{Kind: Missing, Key: "B", Line: 1, Column: 9}, {Kind: Missing, Key: "C", Line: 2, Column: 3}},
		},
		{name: "unterminated", in: "a: <::A", want: []Problem{{Kind: Malformed, Line: 1, Column: 4}}},
		{name: "empty key", in: "<::::>", want: []Problem{{Kind: Malformed, Line: 1, Column: 1}}},
		{name: "key with hyphen", in: "<::my-key::>", want: []Problem{{Kind: Malformed, Line: 1, Column: 1}}},
		{name: "key starting with digit", in: "<::1A::>", want: []Problem{{Kind: Malformed, Line: 1, Column: 1}}},
		{name: "space inside token", in: "<:: A ::>", want: []Problem{{Kind: Malformed, Line: 1, Column: 1}}},
		{
			name: "stray open before a valid token",
			in:   "<::<::A::>",
			want: []Problem{{Kind: Malformed, Line: 1, Column: 1}},
		},
		{
			name: "overlong key",
			in:   "<::" + strings.Repeat("K", MaxKeyLen+1) + "::>",
			want: []Problem{{Kind: Malformed, Line: 1, Column: 1}},
		},
		{
			name: "crlf counts lines by newline",
			in:   "a\r\nb\r\n<::B::>",
			want: []Problem{{Kind: Missing, Key: "B", Line: 3, Column: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := e.Render([]byte(tt.in))
			assert.Nil(t, res.Output)
			assert.Equal(t, tt.want, res.Problems)
			assert.False(t, res.Truncated)
		})
	}
}

func TestRenderMaxKeyLenIsAccepted(t *testing.T) {
	key := strings.Repeat("K", MaxKeyLen)
	e := newEngine(t, Config{Values: map[string]string{key: "v"}})
	res := e.Render([]byte("<::" + key + "::>"))
	require.Empty(t, res.Problems)
	assert.Equal(t, "v", string(res.Output))
}

func TestRenderTruncatesProblems(t *testing.T) {
	e := newEngine(t, Config{})
	res := e.Render([]byte(strings.Repeat("<::X::>", MaxProblems+5)))
	assert.Len(t, res.Problems, MaxProblems)
	assert.True(t, res.Truncated)
	assert.Nil(t, res.Output)
}

func TestRenderMaxOutput(t *testing.T) {
	e := newEngine(t, Config{Values: map[string]string{"A": strings.Repeat("x", 10)}, MaxOutput: 25})

	res := e.Render([]byte("<::A::><::A::>"))
	require.Empty(t, res.Problems)
	assert.Len(t, res.Output, 20)

	res = e.Render([]byte("<::A::><::A::><::A::>"))
	assert.Equal(t, []Problem{{Kind: OutputTooLarge, Line: 1, Column: 15}}, res.Problems)
	assert.Nil(t, res.Output)

	res = e.Render([]byte("<::A::>" + strings.Repeat("-", 20)))
	require.Len(t, res.Problems, 1)
	assert.Equal(t, OutputTooLarge, res.Problems[0].Kind)
}

func TestRenderCustomSyntax(t *testing.T) {
	e := newEngine(t, Config{Syntax: Syntax{Open: "%%", Close: "%%"}, Values: map[string]string{"A": "x"}})
	res := e.Render([]byte("%%A%% and %%A%%"))
	require.Empty(t, res.Problems)
	assert.Equal(t, "x and x", string(res.Output))
}

func TestSyntaxValidate(t *testing.T) {
	tests := []struct {
		name    string
		syntax  Syntax
		wantErr string
	}{
		{name: "default", syntax: DefaultSyntax},
		{name: "braces", syntax: Syntax{Open: "{{", Close: "}}"}},
		{name: "empty open", syntax: Syntax{Close: "}}"}, wantErr: "must not be empty"},
		{name: "empty close", syntax: Syntax{Open: "{{"}, wantErr: "must not be empty"},
		{name: "whitespace", syntax: Syntax{Open: "{{ ", Close: "}}"}, wantErr: "whitespace"},
		{name: "open ends with key byte", syntax: Syntax{Open: "$x", Close: "}"}, wantErr: "must not end"},
		{name: "close starts with key byte", syntax: Syntax{Open: "{", Close: "_}"}, wantErr: "must not start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.syntax.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"A", "_", "a_b", "GROUP_2", strings.Repeat("k", MaxKeyLen)} {
		assert.True(t, ValidKey(k), k)
	}
	for _, k := range []string{"", "1A", "a-b", "a b", "ä", strings.Repeat("k", MaxKeyLen+1)} {
		assert.False(t, ValidKey(k), k)
	}
}

func TestNewRejectsInvalidKeys(t *testing.T) {
	_, err := New(Config{Syntax: DefaultSyntax, Values: map[string]string{"bad-key": "x"}})
	assert.ErrorContains(t, err, `invalid key "bad-key"`)
}

func TestNewCopiesValues(t *testing.T) {
	values := map[string]string{"A": "x"}
	e := newEngine(t, Config{Values: values})
	values["A"] = "changed"
	assert.Equal(t, "x", string(e.Render([]byte("<::A::>")).Output))
}

// FuzzRender checks the scanner against an independent regexp implementation:
// whenever the scanner reports no problem, both must produce the same output,
// and the input must never be modified.
func FuzzRender(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "<::A::>", "<::A::><::B::>", "<::A", "<::::>", "a<::A::>b<::AB::>c",
		"<::<::A::>", "::><::A::>::>", "<::A::>\n<::B::>\r\n", "\x00<::B::>\xff",
	} {
		f.Add([]byte(seed))
	}
	values := map[string]string{"A": "<::B::>", "B": "", "AB": "ab"}
	e := newEngine(f, Config{Values: values})
	tokenRE := regexp.MustCompile(regexp.QuoteMeta(DefaultSyntax.Open) + `([A-Za-z_][A-Za-z0-9_]*)` + regexp.QuoteMeta(DefaultSyntax.Close))

	f.Fuzz(func(t *testing.T, src []byte) {
		orig := bytes.Clone(src)
		res := e.Render(src)
		require.Equal(t, orig, src, "input modified")
		if len(res.Problems) > 0 {
			require.Nil(t, res.Output)
			return
		}
		want := tokenRE.ReplaceAllFunc(src, func(m []byte) []byte {
			key := string(m[len(DefaultSyntax.Open) : len(m)-len(DefaultSyntax.Close)])
			v, ok := values[key]
			require.True(t, ok, "scanner accepted unknown key %q", key)
			return []byte(v)
		})
		require.Equal(t, string(want), string(res.Output))
	})
}
