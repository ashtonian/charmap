// Package tree renders every selected file under a directory.
//
// A run has three phases. Walk selects regular files by root-relative path,
// pruning excluded directories and every .git directory. Each selected file
// is then read and rendered; in Apply, a changed file's output is written to
// a temporary file beside it. Only when every file rendered without a problem
// or error are the temporary files renamed over their targets. A problem in
// any file therefore leaves the whole tree untouched.
//
// Every file is opened through an os.Root, so no path can resolve outside the
// directory. Symlinks and other non-regular files are never read or written.
package tree

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/ashtonian/charmap/v2/internal/token"
)

// maxReportedErrors bounds how many per-file errors a run returns.
const maxReportedErrors = 20

// Renderer renders one file's content.
type Renderer interface {
	Render(src []byte) token.Result
}

// Options configures a run.
type Options struct {
	// Include selects files whose root-relative, slash-separated path matches
	// any pattern. Empty selects every file.
	Include []*regexp.Regexp
	// Exclude skips files and prunes directories whose root-relative path
	// matches any pattern. A directory is tested both as "dir" and "dir/".
	Exclude []*regexp.Regexp
	// Workers is the number of files processed concurrently; must be positive.
	Workers int
	// MaxFileSize is the largest file read, in bytes; must be positive.
	MaxFileSize int64
	// Logger receives per-file debug events. Nil discards them.
	Logger *slog.Logger
}

// Problem is a token problem in one file.
type Problem struct {
	Path string
	token.Problem
}

// Report summarizes a run.
type Report struct {
	// Files is the number of files selected.
	Files int
	// Changed lists the files whose content changed, sorted. In Check they
	// would change; in Apply they were written.
	Changed []string
	// Problems lists token problems sorted by path, then position.
	Problems []Problem
	// Truncated reports that at least one file had more than
	// token.MaxProblems problems.
	Truncated bool
}

// Check renders every selected file under dir and reports what would change,
// without writing anything.
func Check(ctx context.Context, dir string, r Renderer, opts Options) (Report, error) {
	return run(ctx, dir, r, opts, false)
}

// Apply renders every selected file under dir and, only if no file has a
// problem or error, replaces each changed file. Each file is replaced
// atomically by rename, but the set of renames is not one transaction: if a
// rename fails, files renamed before it stay replaced and the error says how
// many.
func Apply(ctx context.Context, dir string, r Renderer, opts Options) (Report, error) {
	return run(ctx, dir, r, opts, true)
}

type fileResult struct {
	path     string      // root-relative, slash-separated
	info     fs.FileInfo // as read; used to detect concurrent modification
	staged   string      // name of the temporary file beside it, if any
	changed  bool
	problems []token.Problem
	trunc    bool
	err      error
}

// filesPerJob bounds how many files of one directory a worker takes at a
// time, so a large flat directory is still spread across workers.
const filesPerJob = 64

// dirJob is a batch of files in one directory, as indexes into the results.
// Workers open the directory once per batch: resolving every file's full path
// through the root instead would reopen each parent directory for every
// Lstat and Open.
type dirJob struct {
	dir   string // root-relative, slash-separated; "." for the root
	files []int
}

func run(ctx context.Context, dir string, r Renderer, opts Options, write bool) (Report, error) {
	if opts.Workers < 1 {
		return Report{}, fmt.Errorf("workers must be positive, got %d", opts.Workers)
	}
	if opts.MaxFileSize < 1 {
		return Report{}, fmt.Errorf("max file size must be positive, got %d", opts.MaxFileSize)
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return Report{}, err
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("%s is not a directory", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Report{}, err
	}
	defer root.Close()

	paths, err := walk(ctx, root, opts, log)
	if err != nil {
		return Report{}, fmt.Errorf("walk %s: %w", dir, err)
	}

	results := process(ctx, root, paths, r, opts, write, log)
	report := summarize(results)

	var errs []error
	for _, res := range results {
		if res.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", res.path, res.err))
		}
	}
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 || len(report.Problems) > 0 {
		discard(root, results, log)
		report.Changed = nil
		return report, joinBounded(errs)
	}
	if !write {
		return report, nil
	}

	written, err := commit(root, results, log)
	report.Changed = written
	return report, err
}

// walk returns the root-relative, slash-separated paths of selected regular
// files in lexical order.
func walk(ctx context.Context, root *os.Root, opts Options, log *slog.Logger) ([]string, error) {
	var paths []string
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if d.IsDir() {
			// Pruned before the directory is read, so an excluded directory
			// may be unreadable.
			if d.Name() == ".git" || matchAny(opts.Exclude, p) || matchAny(opts.Exclude, p+"/") {
				log.Debug("skipping directory", slog.String("path", p))
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			log.Debug("skipping non-regular file", slog.String("path", p), slog.String("type", d.Type().String()))
			return nil
		}
		if matchAny(opts.Exclude, p) || (len(opts.Include) > 0 && !matchAny(opts.Include, p)) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	return paths, err
}

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// process renders every path with a bounded worker pool. Once any file fails,
// later files are still rendered so every problem is reported, but no more
// output is staged.
func process(ctx context.Context, root *os.Root, paths []string, r Renderer, opts Options, write bool, log *slog.Logger) []fileResult {
	results := make([]fileResult, len(paths))
	for i, p := range paths {
		results[i].path = p
	}
	jobs := batchByDir(paths)

	var failed atomic.Bool
	ch := make(chan dirJob)
	var wg sync.WaitGroup
	for range min(opts.Workers, len(jobs)) {
		wg.Go(func() {
			for job := range ch {
				processDir(ctx, root, job, results, r, opts.MaxFileSize, write, &failed, log)
			}
		})
	}
feed:
	for _, job := range jobs {
		select {
		case ch <- job:
		case <-ctx.Done():
			break feed
		}
	}
	close(ch)
	wg.Wait()
	return results
}

// batchByDir groups path indexes by directory, in order of first appearance,
// splitting each directory into batches of at most filesPerJob.
func batchByDir(paths []string) []dirJob {
	var jobs []dirJob
	open := make(map[string]int) // directory -> index of its batch being filled
	for i, p := range paths {
		dir := path.Dir(p)
		j, ok := open[dir]
		if !ok || len(jobs[j].files) == filesPerJob {
			jobs = append(jobs, dirJob{dir: dir})
			j = len(jobs) - 1
			open[dir] = j
		}
		jobs[j].files = append(jobs[j].files, i)
	}
	return jobs
}

func processDir(ctx context.Context, root *os.Root, job dirJob, results []fileResult, r Renderer, maxSize int64, write bool, failed *atomic.Bool, log *slog.Logger) {
	dir, err := root.OpenRoot(filepath.FromSlash(job.dir))
	if err != nil {
		for _, i := range job.files {
			results[i].err = err
		}
		failed.Store(true)
		return
	}
	defer dir.Close()
	for _, i := range job.files {
		if ctx.Err() != nil {
			return // run reports the cancellation once
		}
		res := &results[i]
		processFile(dir, res, r, maxSize, write, failed)
		if res.err != nil || len(res.problems) > 0 {
			failed.Store(true)
		}
		if res.changed {
			log.Debug("rendered file", slog.String("path", res.path), slog.Bool("staged", res.staged != ""))
		}
	}
}

func processFile(dir *os.Root, res *fileResult, r Renderer, maxSize int64, write bool, failed *atomic.Bool) {
	name := path.Base(res.path)
	data, info, err := readRegular(dir, name, maxSize)
	if err != nil {
		res.err = err
		return
	}
	res.info = info

	out := r.Render(data)
	if len(out.Problems) > 0 {
		res.problems, res.trunc = out.Problems, out.Truncated
		return
	}
	if out.Replaced == 0 || bytes.Equal(out.Output, data) {
		return
	}
	res.changed = true
	if !write || failed.Load() {
		return
	}
	res.staged, res.err = stage(dir, name, out.Output, info)
}

// readRegular reads name only if it is, and stays, the same regular file the
// walk found. Lstat refuses a symlink at the final component, SameFile
// detects a swap between Lstat and open, and the open never blocks on a FIFO.
func readRegular(dir *os.Root, name string, maxSize int64) ([]byte, fs.FileInfo, error) {
	before, err := dir.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("not a regular file (%s)", before.Mode().Type())
	}
	if before.Size() > maxSize {
		return nil, nil, fmt.Errorf("file is %d bytes, over the %d byte limit", before.Size(), maxSize)
	}
	f, err := dir.OpenFile(name, openReadFlags, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(before, opened) {
		return nil, nil, errors.New("file was replaced while being read")
	}
	data, err := readAll(f, opened.Size(), maxSize)
	if err != nil {
		return nil, nil, err
	}
	return data, opened, nil
}

// readAll reads f to EOF into a buffer sized from the file's stat size, so a
// file that does not change while being read costs one allocation.
func readAll(f *os.File, size, maxSize int64) ([]byte, error) {
	data := make([]byte, 0, size+1) // +1 lets EOF be seen without growing
	for {
		if len(data) == cap(data) {
			data = append(data, 0)[:len(data)]
		}
		n, err := f.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if int64(len(data)) > maxSize {
			return nil, fmt.Errorf("file grew past the %d byte limit while being read", maxSize)
		}
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// stage writes data to a new temporary file in name's directory with name's
// permission bits and owner, and syncs it so a later rename never exposes a
// partly written file.
func stage(dir *os.Root, name string, data []byte, orig fs.FileInfo) (tmp string, err error) {
	tmp = "." + name + ".charmap-" + rand.Text()[:12] + ".tmp"
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = dir.Remove(tmp)
			tmp = ""
		}
	}()
	if _, err = f.Write(data); err != nil {
		return tmp, fmt.Errorf("write temporary file: %w", err)
	}
	// Chmod on the descriptor sets the exact bits regardless of umask.
	if err = f.Chmod(orig.Mode().Perm()); err != nil {
		return tmp, fmt.Errorf("set permissions: %w", err)
	}
	if err = matchOwner(f, orig); err != nil {
		return tmp, err
	}
	if err = f.Sync(); err != nil {
		return tmp, fmt.Errorf("sync temporary file: %w", err)
	}
	if err = f.Close(); err != nil {
		return tmp, fmt.Errorf("close temporary file: %w", err)
	}
	return tmp, nil
}

// commit renames staged files over their targets in path order. A target
// modified since it was read is not overwritten.
func commit(root *os.Root, results []fileResult, log *slog.Logger) ([]string, error) {
	var (
		written []string
		dir     *os.Root
		dirName string
	)
	// Directory handles are read-only; a close error has nothing to undo.
	defer func() {
		if dir != nil {
			_ = dir.Close()
		}
	}()
	for i, res := range results {
		if res.staged == "" {
			continue
		}
		var err error
		if d := path.Dir(res.path); dir == nil || d != dirName {
			if dir != nil {
				_ = dir.Close()
			}
			dir, err = root.OpenRoot(filepath.FromSlash(d))
			dirName = d
		}
		name := path.Base(res.path)
		if err == nil {
			err = unchangedSince(dir, name, res.info)
		}
		if err == nil {
			err = dir.Rename(res.staged, name)
		}
		if err != nil {
			discard(root, results[i:], log)
			return written, fmt.Errorf("%s: %w (%d changed files were written before this one)", res.path, err, len(written))
		}
		written = append(written, res.path)
	}
	return written, nil
}

func unchangedSince(dir *os.Root, name string, read fs.FileInfo) error {
	now, err := dir.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(now, read) || now.Size() != read.Size() || !now.ModTime().Equal(read.ModTime()) {
		return errors.New("file changed during the run; not overwritten")
	}
	return nil
}

// discard removes staged temporary files.
func discard(root *os.Root, results []fileResult, log *slog.Logger) {
	for _, res := range results {
		if res.staged == "" {
			continue
		}
		tmp := path.Join(path.Dir(res.path), res.staged)
		if err := root.Remove(filepath.FromSlash(tmp)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("could not remove temporary file", slog.String("path", tmp), slog.Any("error", err))
		}
	}
}

func summarize(results []fileResult) Report {
	report := Report{Files: len(results)}
	for _, res := range results {
		if res.changed {
			report.Changed = append(report.Changed, res.path)
		}
		for _, p := range res.problems {
			report.Problems = append(report.Problems, Problem{Path: res.path, Problem: p})
		}
		report.Truncated = report.Truncated || res.trunc
	}
	// Results are already in path order and each file's problems in input
	// order; the sort keeps the contract explicit.
	slices.SortStableFunc(report.Problems, func(a, b Problem) int {
		if a.Path != b.Path {
			if a.Path < b.Path {
				return -1
			}
			return 1
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Column - b.Column
	})
	return report
}

func joinBounded(errs []error) error {
	if len(errs) <= maxReportedErrors {
		return errors.Join(errs...)
	}
	more := fmt.Errorf("and %d more errors", len(errs)-maxReportedErrors)
	return errors.Join(append(errs[:maxReportedErrors:maxReportedErrors], more)...)
}
