//go:build unix

package tree

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFIFOIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.yaml": "<::A::>"})
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "pipe.yaml"), 0o644))

	report, err := Apply(context.Background(), dir, engine(t, map[string]string{"A": "x"}), options())
	require.NoError(t, err)
	assert.Equal(t, 1, report.Files)
	assert.Equal(t, []string{"a.yaml"}, report.Changed)
}

func TestApplyKeepsOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(path, []byte("<::A::>"), 0o644))
	before, err := os.Stat(path)
	require.NoError(t, err)

	_, err = Apply(context.Background(), dir, engine(t, map[string]string{"A": "x"}), options())
	require.NoError(t, err)
	after, err := os.Stat(path)
	require.NoError(t, err)
	b, a := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	assert.Equal(t, b.Uid, a.Uid)
	assert.Equal(t, b.Gid, a.Gid)
}
