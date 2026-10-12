//go:build !unix

package tree

import (
	"io/fs"
	"os"
)

const openReadFlags = os.O_RDONLY

func matchOwner(*os.File, fs.FileInfo) error { return nil }
