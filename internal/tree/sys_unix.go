//go:build unix

package tree

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// O_NONBLOCK keeps a FIFO swapped in after the walk from blocking the open;
// it has no effect on reads from a regular file.
const openReadFlags = os.O_RDONLY | syscall.O_NONBLOCK

// matchOwner gives f the owner and group of orig. It fails rather than
// silently changing ownership when the process may not chown.
func matchOwner(f *os.File, orig fs.FileInfo) error {
	want, ok := orig.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	cur, err := f.Stat()
	if err != nil {
		return err
	}
	have, ok := cur.Sys().(*syscall.Stat_t)
	if !ok || (have.Uid == want.Uid && have.Gid == want.Gid) {
		return nil
	}
	if err := f.Chown(int(want.Uid), int(want.Gid)); err != nil {
		return fmt.Errorf("preserve owner %d:%d: %w", want.Uid, want.Gid, err)
	}
	return nil
}
