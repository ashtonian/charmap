package tree

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/charmap/v2/internal/token"
)

var yaml = []*regexp.Regexp{regexp.MustCompile(`\.ya?ml$`)}

func engine(t *testing.T, values map[string]string) *token.Engine {
	t.Helper()
	e, err := token.New(token.Config{Syntax: token.DefaultSyntax, Values: values})
	require.NoError(t, err)
	return e
}

func options() Options {
	return Options{Include: yaml, Workers: 4, MaxFileSize: 1 << 20}
}

// writeTree creates files under dir; keys are slash-separated paths.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// assertNoTemporaryFiles fails if a staged file was left behind.
func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		assert.NotContains(t, d.Name(), ".charmap-", "temporary file left at %s", p)
		return nil
	})
	require.NoError(t, err)
}

func TestApplyReplacesSelectedFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"a.yaml":      "group: <::GROUP::>\n",
		"sub/b.yml":   "cluster: <::CLUSTER::>\n",
		"notes.txt":   "<::UNSET::> is ignored: not selected\n",
		"plain.yaml":  "no tokens\n",
		"sub/c.yaml":  "both: <::GROUP::>-<::CLUSTER::>\n",
		"deep/x/y.ya": "not selected\n",
	})

	report, err := Apply(context.Background(), dir, engine(t, map[string]string{"GROUP": "us-east", "CLUSTER": "c1"}), options())
	require.NoError(t, err)
	assert.Empty(t, report.Problems)
	assert.Equal(t, 4, report.Files)
	assert.Equal(t, []string{"a.yaml", "sub/b.yml", "sub/c.yaml"}, report.Changed)

	assert.Equal(t, "group: us-east\n", readFile(t, filepath.Join(dir, "a.yaml")))
	assert.Equal(t, "cluster: c1\n", readFile(t, filepath.Join(dir, "sub/b.yml")))
	assert.Equal(t, "both: us-east-c1\n", readFile(t, filepath.Join(dir, "sub/c.yaml")))
	assert.Equal(t, "<::UNSET::> is ignored: not selected\n", readFile(t, filepath.Join(dir, "notes.txt")))
	assertNoTemporaryFiles(t, dir)
}

func TestApplyLeavesTreeUntouchedWhenAnyFileHasAProblem(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"z-bad.yaml": "x: <::MISSING::>\n"}
	for i := range 20 {
		files[filepath.ToSlash(filepath.Join("good", strings.Repeat("g", i+1)+".yaml"))] = "group: <::GROUP::>\n"
	}
	writeTree(t, dir, files)

	report, err := Apply(context.Background(), dir, engine(t, map[string]string{"GROUP": "us-east"}), options())
	require.NoError(t, err)
	require.Equal(t, []Problem{{Path: "z-bad.yaml", Problem: token.Problem{Kind: token.Missing, Key: "MISSING", Line: 1, Column: 4}}}, report.Problems)
	assert.Empty(t, report.Changed)
	for p, content := range files {
		assert.Equal(t, content, readFile(t, filepath.Join(dir, filepath.FromSlash(p))), p)
	}
	assertNoTemporaryFiles(t, dir)
}

func TestApplyReportsProblemsSortedAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"b.yaml": "<::X::>\n<::Y::>",
		"a.yaml": "ok\n  <::Z::>",
	})
	report, err := Apply(context.Background(), dir, engine(t, nil), options())
	require.NoError(t, err)
	var got []string
	for _, p := range report.Problems {
		got = append(got, p.Path+":"+p.Key)
	}
	assert.Equal(t, []string{"a.yaml:Z", "b.yaml:X", "b.yaml:Y"}, got)
}

func TestCheckWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.yaml": "group: <::GROUP::>\n"})

	report, err := Check(context.Background(), dir, engine(t, map[string]string{"GROUP": "us-east"}), options())
	require.NoError(t, err)
	assert.Equal(t, []string{"a.yaml"}, report.Changed)
	assert.Equal(t, "group: <::GROUP::>\n", readFile(t, filepath.Join(dir, "a.yaml")))
	assertNoTemporaryFiles(t, dir)
}

func TestApplyPreservesPermissionBits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(path, []byte("<::A::>"), 0o600))
	require.NoError(t, os.Chmod(path, 0o640))

	_, err := Apply(context.Background(), dir, engine(t, map[string]string{"A": "x"}), options())
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o640), info.Mode().Perm())
}

func TestApplyDoesNotRewriteUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"plain.yaml": "no tokens\n"})
	before, err := os.Stat(filepath.Join(dir, "plain.yaml"))
	require.NoError(t, err)

	_, err = Apply(context.Background(), dir, engine(t, nil), options())
	require.NoError(t, err)
	after, err := os.Stat(filepath.Join(dir, "plain.yaml"))
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after))
	assert.Equal(t, before.ModTime(), after.ModTime())
}

func TestSymlinksAreNeverFollowed(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "work")
	outside := filepath.Join(base, "outside")
	writeTree(t, dir, map[string]string{"real.yaml": "<::A::>"})
	writeTree(t, outside, map[string]string{"target.yaml": "<::A::>", "sub/x.yaml": "<::A::>"})
	require.NoError(t, os.Symlink(filepath.Join(outside, "target.yaml"), filepath.Join(dir, "link.yaml")))
	require.NoError(t, os.Symlink("real.yaml", filepath.Join(dir, "inner-link.yaml")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "sub"), filepath.Join(dir, "linkdir")))

	report, err := Apply(context.Background(), dir, engine(t, map[string]string{"A": "changed"}), options())
	require.NoError(t, err)
	assert.Equal(t, []string{"real.yaml"}, report.Changed)
	assert.Equal(t, "<::A::>", readFile(t, filepath.Join(outside, "target.yaml")))
	assert.Equal(t, "<::A::>", readFile(t, filepath.Join(outside, "sub/x.yaml")))

	linkTarget, err := os.Readlink(filepath.Join(dir, "inner-link.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "real.yaml", linkTarget, "symlink itself must be left alone")
}

func TestHardLinkedFileDoesNotChangeItsOtherName(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "work")
	writeTree(t, dir, map[string]string{"a.yaml": "<::A::>"})
	other := filepath.Join(base, "other.yaml")
	require.NoError(t, os.Link(filepath.Join(dir, "a.yaml"), other))

	_, err := Apply(context.Background(), dir, engine(t, map[string]string{"A": "changed"}), options())
	require.NoError(t, err)
	assert.Equal(t, "changed", readFile(t, filepath.Join(dir, "a.yaml")))
	assert.Equal(t, "<::A::>", readFile(t, other))
}

func TestGitAndExcludedDirectoriesArePruned(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".git/x.yaml":         "<::MISSING::>",
		"nested/.git/y.yaml":  "<::MISSING::>",
		"vendor/z.yaml":       "<::MISSING::>",
		"deep/vendor/w.yaml":  "<::MISSING::>",
		"skip-me.yaml":        "<::MISSING::>",
		"keep/vendor.yaml":    "<::A::>",
		"locked/secret.yaml":  "<::MISSING::>",
		"unrelated/file.yaml": "<::A::>",
	})
	locked := filepath.Join(dir, "locked")
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	opts := options()
	opts.Exclude = []*regexp.Regexp{
		regexp.MustCompile(`(^|/)vendor/`),
		regexp.MustCompile(`^skip-me\.yaml$`),
		regexp.MustCompile(`^locked$`),
	}
	report, err := Apply(context.Background(), dir, engine(t, map[string]string{"A": "x"}), opts)
	require.NoError(t, err)
	assert.Empty(t, report.Problems)
	assert.Equal(t, []string{"keep/vendor.yaml", "unrelated/file.yaml"}, report.Changed)
}

func TestIncludeSelectsByRootRelativePath(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.json": "<::A::>", "sub/b.json": "<::A::>", "c.yaml": "<::A::>"})
	opts := options()
	opts.Include = []*regexp.Regexp{regexp.MustCompile(`^sub/.*\.json$`)}

	report, err := Apply(context.Background(), dir, engine(t, map[string]string{"A": "x"}), opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"sub/b.json"}, report.Changed)
}

func TestFileOverSizeLimitFailsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.yaml": "<::A::>", "big.yaml": strings.Repeat("x", 100) + "<::A::>"})
	opts := options()
	opts.MaxFileSize = 50

	_, err := Apply(context.Background(), dir, engine(t, map[string]string{"A": "x"}), opts)
	require.ErrorContains(t, err, "big.yaml: file is 107 bytes, over the 50 byte limit")
	assert.Equal(t, "<::A::>", readFile(t, filepath.Join(dir, "a.yaml")))
	assertNoTemporaryFiles(t, dir)
}

func TestCancelledRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.yaml": "<::A::>"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Apply(ctx, dir, engine(t, map[string]string{"A": "x"}), options())
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "<::A::>", readFile(t, filepath.Join(dir, "a.yaml")))
	assertNoTemporaryFiles(t, dir)
}

// modifyingRenderer rewrites a file on disk while it is being rendered, as a
// concurrent writer would.
type modifyingRenderer struct {
	*token.Engine
	path string
}

func (m modifyingRenderer) Render(src []byte) token.Result {
	if err := os.WriteFile(m.path, []byte("concurrent edit, longer than before"), 0o644); err != nil {
		panic(err)
	}
	return m.Engine.Render(src)
}

func TestFileModifiedDuringRunIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.yaml": "<::A::>"})
	path := filepath.Join(dir, "a.yaml")
	r := modifyingRenderer{Engine: engine(t, map[string]string{"A": "x"}), path: path}

	_, err := Apply(context.Background(), dir, r, options())
	require.ErrorContains(t, err, "a.yaml: file changed during the run; not overwritten (0 changed files were written before this one)")
	assert.Equal(t, "concurrent edit, longer than before", readFile(t, path))
	assertNoTemporaryFiles(t, dir)
}

func TestRunValidatesArguments(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.yaml")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	e := engine(t, nil)

	_, err := Apply(context.Background(), file, e, options())
	assert.ErrorContains(t, err, "is not a directory")

	_, err = Apply(context.Background(), filepath.Join(dir, "missing"), e, options())
	assert.ErrorIs(t, err, fs.ErrNotExist)

	opts := options()
	opts.Workers = 0
	_, err = Apply(context.Background(), dir, e, opts)
	assert.ErrorContains(t, err, "workers must be positive")

	opts = options()
	opts.MaxFileSize = 0
	_, err = Apply(context.Background(), dir, e, opts)
	assert.ErrorContains(t, err, "max file size must be positive")
}
