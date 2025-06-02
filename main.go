package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

var (
	openDelim            = flag.String("open", "<::", "opening delimiter")
	closeDelim           = flag.String("close", "::>", "closing delimiter")
	targetDir            = flag.String("dir", ".", "directory to scan")
	workers              = flag.Int("workers", runtime.GOMAXPROCS(0), "concurrent file processors")
	mode                 = flag.String("both", "env", "value source: env | flag | both")
	logFile              = flag.String("log", "", "log file (default no logging)")
	inc                  = sliceFlag{`.*\.ya?ml$`}
	ign                  = sliceFlag{`^\.git(/|$)`}
	userKV     StringMap = make(StringMap)
)

func init() {
	flag.Var(&inc, "include", "regex for files to process (default: .*\\.ya?ml$)")
	flag.Var(&ign, "ignore", "regex for files/dirs to skip (default: ^\\.git(/|$))")
	flag.Var(&userKV, "set", "override in KEY=value form (may be repeated)")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `
charmap scans every regular file under -dir (default ".") and replaces
instances of %sKEY%s. Anchors are configurable with -open and -close flags.

charmap can read key values from environment variables, command line flags,
or both. Use -mode to select the source:
  -mode env   : read from environment variables only (will read all env vars)
  -mode flag  : read from command line flags only (faster)
  -mode both  : read from both environment variables and command line flags

Example:
  preprocess -set PUBLIC_DOMAIN=example.com -mode=both

Flags:
`, *openDelim, *closeDelim)
		flag.PrintDefaults()
	}
}

type config struct {
	OpenDelim  string
	CloseDelim string
	TargetDir  string
	Workers    int
	Mode       string
	LogFile    string
	CloseLog   func()
	FileFilter *fileFilter
	KeyMap     StringMap
}

func parseConfig() (config, error) {
	flag.Parse()

	values := make(map[string]string)
	switch *mode {
	case "env":
		for _, kv := range os.Environ() {
			if idx := strings.IndexByte(kv, '='); idx != -1 {
				values[kv[:idx]] = kv[idx+1:]
			}
		}
	case "flag":
		for k, v := range userKV {
			values[k] = v
		}
	case "both":
		for _, kv := range os.Environ() {
			if idx := strings.IndexByte(kv, '='); idx != -1 {
				values[kv[:idx]] = kv[idx+1:]
			}
		}
		for k, v := range userKV {
			values[k] = v
		}
	default:
		return config{}, fmt.Errorf("invalid mode %q, must be one of: env, flag, both", *mode)
	}

	if len(*openDelim) == 0 || len(*closeDelim) == 0 {
		return config{}, fmt.Errorf("delimiters must not be empty")
	}
	if *workers <= 0 {
		return config{}, fmt.Errorf("workers must be greater than 0, got %d", *workers)
	}
	if *targetDir == "" {
		return config{}, fmt.Errorf("target directory must not be empty")
	}
	if _, err := os.Stat(*targetDir); os.IsNotExist(err) {
		return config{}, fmt.Errorf("target directory %q does not exist", *targetDir)
	}
	if fi, err := os.Stat(*targetDir); err != nil || !fi.IsDir() {
		return config{}, fmt.Errorf("target %q is not a directory", *targetDir)
	}

	fileFilter, err := newFileFilter(inc, ign)
	if err != nil {
		return config{}, fmt.Errorf("failed to create file filter: %w", err)
	}

	closer := func() {}
	slog.SetDefault(slog.New(discardHandler{}))
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return config{}, fmt.Errorf("failed to open log file %q: %w", *logFile, err)
		}

		closer = func() {
			f.Close()
		}

		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})))
	}

	cfg := config{
		OpenDelim:  *openDelim,
		CloseDelim: *closeDelim,
		TargetDir:  *targetDir,
		Workers:    *workers,
		Mode:       *mode,
		LogFile:    *logFile,
		CloseLog:   closer,
		FileFilter: fileFilter,
		KeyMap:     values,
	}
	return cfg, nil
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	defer cfg.CloseLog()

	slog.Info("charmap started",
		slog.String("dir", cfg.TargetDir),
		slog.Int("workers", cfg.Workers),
		slog.String("mode", cfg.Mode),
		slog.String("open", cfg.OpenDelim),
		slog.String("close", cfg.CloseDelim),
		slog.String("logfile", cfg.LogFile),
		slog.String("values", cfg.KeyMap.String()),
		slog.String("include", inc.String()),
		slog.String("ignore", ign.String()),
	)

	err = processFiles(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func processFiles(cfg config) error {
	files := make(chan string, cfg.Workers*2)
	errs := []error{}
	errLock := sync.Mutex{}

	replacer := buildNewReplacer([]byte(cfg.OpenDelim), []byte(cfg.CloseDelim), cfg.KeyMap)

	var wg sync.WaitGroup

	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range files {
				err := processFile(path, replacer)
				if err != nil {
					errLock.Lock()
					errs = append(errs, fmt.Errorf("failed to process %q: %w", path, err))
					errLock.Unlock()
					slog.Error("error processing file", slog.String("path", path), slog.Any("error", err))
				}
			}
		}()
	}

	go func() {
		err := filepath.WalkDir(cfg.TargetDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}

			if !cfg.FileFilter.match(p) {
				slog.Debug("skipping file", slog.String("path", p))
				return nil
			}
			files <- p
			return nil
		})
		if err != nil {
			errLock.Lock()
			errs = append(errs, fmt.Errorf("failed to walk directory %q: %w", cfg.TargetDir, err))
			errLock.Unlock()
		}
		close(files)
	}()

	wg.Wait()

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func processFile(path string, replacer replacer) error {
	in, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fi, _ := os.Stat(path)

	out, changed, err := replacer(in)
	if err != nil {
		return fmt.Errorf("failed to process %q: %w", path, err)
	}

	if changed {
		slog.Info("processed file", slog.String("path", path), slog.Int("size", len(out)),
			slog.Int("original_size", len(in)), slog.Bool("changed", changed),
		)
		return os.WriteFile(path, out, fi.Mode())
	}

	slog.Debug("no changes made to file", slog.String("path", path))
	return nil
}

type replacer func(txt []byte) ([]byte, bool, error)

func buildNewReplacer(open, close []byte, values map[string]string) replacer {
	openStr, closeStr := string(open), string(close)
	pairs := make([]string, 0, len(values)*2)

	for k, v := range values {
		pairs = append(pairs, openStr+k+closeStr, v)
	}
	strReplacer := strings.NewReplacer(pairs...)

	fn := func(txt []byte) ([]byte, bool, error) {
		out := strReplacer.Replace(string(txt))
		changed := out != string(txt)

		if idx := strings.Index(out, openStr); idx != -1 {
			start := idx + len(openStr)
			if end := strings.Index(out[start:], closeStr); end != -1 {
				missing := out[start : start+end]
				return nil, false, fmt.Errorf("env/flag %q not set", missing)
			}
		}

		return []byte(out), changed, nil
	}
	return fn
}

type sliceFlag []string

func (s *sliceFlag) String() string     { return fmt.Sprint([]string(*s)) }
func (s *sliceFlag) Set(v string) error { *s = append(*s, v); return nil }

type fileFilter struct {
	includes []*regexp.Regexp
	excludes []*regexp.Regexp
}

func newFileFilter(incPat, excPat []string) (*fileFilter, error) {
	compileAll := func(pats []string) ([]*regexp.Regexp, error) {
		out := make([]*regexp.Regexp, 0, len(pats))
		for _, p := range pats {
			rx, err := regexp.Compile(p)
			if err != nil {
				return nil, err
			}
			out = append(out, rx)
		}
		return out, nil
	}
	inc, err := compileAll(incPat)
	if err != nil {
		return nil, fmt.Errorf("include: %w", err)
	}
	exc, err := compileAll(excPat)
	if err != nil {
		return nil, fmt.Errorf("ignore: %w", err)
	}
	return &fileFilter{includes: inc, excludes: exc}, nil
}

func (f *fileFilter) match(path string) bool {
	for _, rx := range f.excludes {
		if rx.MatchString(path) {
			return false
		}
	}
	if len(f.includes) == 0 {
		return true
	}
	for _, rx := range f.includes {
		if rx.MatchString(path) {
			return true
		}
	}
	return false
}

type StringMap map[string]string

func (m *StringMap) String() string {
	return fmt.Sprintf("%v", *m)
}

func (m *StringMap) Set(value string) error {
	pairs := strings.Split(value, ",")
	if *m == nil {
		*m = make(map[string]string)
	}
	for _, pair := range pairs {
		parts := strings.Split(pair, "=")
		if len(parts) == 2 {
			(*m)[parts[0]] = parts[1]
		} else {
			return fmt.Errorf("invalid key=value pair %q, expected format KEY=VALUE", pair)
		}
	}
	return nil
}

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs(_ []slog.Attr) slog.Handler      { return discardHandler{} }
func (discardHandler) WithGroup(_ string) slog.Handler           { return discardHandler{} }
