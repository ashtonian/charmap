// Command charmap replaces <::KEY::> tokens in files with values named
// explicitly on the command line. See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/ashtonian/charmap/v2/internal/token"
	"github.com/ashtonian/charmap/v2/internal/tree"
	"github.com/ashtonian/charmap/v2/internal/values"
)

// version is set at build time with -ldflags "-X main.version=...".
var version string

const (
	exitOK     = 0
	exitFailed = 1 // token problems, I/O errors, interruption
	exitUsage  = 2 // invalid flags or configuration
)

const (
	defaultMaxFileSize = 64 << 20
	defaultInclude     = `\.ya?ml$`
	maxWorkers         = 1024
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal, restore default handling so a second one
	// terminates immediately, e.g. while filter is blocked reading stdin.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "render", "check":
		return runTree(ctx, cmd, rest, environ, stderr)
	case "filter":
		return runFilter(rest, environ, stdin, stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, versionString())
		return exitOK
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "charmap: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

const usage = `charmap replaces <::KEY::> tokens in files with values you name explicitly.

Usage:
  charmap render [flags]   rewrite selected files under --dir
  charmap check  [flags]   report problems and files that would change; write nothing
  charmap filter [flags]   render stdin to stdout
  charmap version          print version, Go version and platform
  charmap help             print this help

Run 'charmap <command> -h' for a command's flags.

Values come only from --set KEY=VALUE, --env NAME and --env-prefix PREFIX.
Exit status: 0 success, 1 token problems or I/O errors, 2 usage errors.
`

// commonFlags are shared by every rendering command.
type commonFlags struct {
	open, close string
	set, env    stringList
	envPrefix   string
	maxFileSize int64
	logLevel    string
	logFormat   string
}

func addCommonFlags(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.open, "open", token.DefaultSyntax.Open, "opening delimiter")
	fs.StringVar(&c.close, "close", token.DefaultSyntax.Close, "closing delimiter")
	fs.Var(&c.set, "set", "`KEY=VALUE` assignment; repeatable; splits at the first '='")
	fs.Var(&c.env, "env", "read the environment variable `NAME` as key NAME; repeatable")
	fs.StringVar(&c.envPrefix, "env-prefix", "", "read every environment variable starting with `PREFIX`; the key is the rest of the name")
	fs.Int64Var(&c.maxFileSize, "max-file-size", defaultMaxFileSize, "largest file read and largest rendered output, in `bytes`")
	fs.StringVar(&c.logLevel, "log-level", "warn", "log level: debug, info, warn or error")
	fs.StringVar(&c.logFormat, "log-format", "text", "log format: text or json")
	return c
}

func (c *commonFlags) logger(w io.Writer) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.logLevel)); err != nil {
		return nil, fmt.Errorf("--log-level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	switch c.logFormat {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("--log-format must be text or json, got %q", c.logFormat)
	}
}

func (c *commonFlags) engine(environ []string) (*token.Engine, error) {
	if c.maxFileSize < 1 || c.maxFileSize > math.MaxInt {
		return nil, fmt.Errorf("--max-file-size must be between 1 and %d, got %d", math.MaxInt, c.maxFileSize)
	}
	vals, err := values.Resolve(values.Sources{EnvPrefix: c.envPrefix, Env: c.env, Set: c.set}, environ)
	if err != nil {
		return nil, err
	}
	return token.New(token.Config{
		Syntax:    token.Syntax{Open: c.open, Close: c.close},
		Values:    vals,
		MaxOutput: int(c.maxFileSize),
	})
}

func runTree(ctx context.Context, cmd string, args, environ []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("charmap "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	common := addCommonFlags(fs)
	dir := fs.String("dir", ".", "directory to process")
	var include, exclude stringList
	fs.Var(&include, "include", "select files whose root-relative path matches `REGEXP`; repeatable; replaces the default "+defaultInclude)
	fs.Var(&exclude, "exclude", "skip files, and prune directories, whose root-relative path matches `REGEXP`; repeatable")
	workers := fs.Int("workers", runtime.GOMAXPROCS(0), "files processed concurrently")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: charmap %s [flags]\n\n", cmd)
		if cmd == "render" {
			fmt.Fprint(fs.Output(), "Rewrites every selected file under --dir. Nothing is written unless every\nfile renders without a problem.\n\n")
		} else {
			fmt.Fprint(fs.Output(), "Reports every problem and each file that would change. Writes nothing.\n\n")
		}
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, stderr); !ok {
		return code
	}
	if *workers < 1 || *workers > maxWorkers {
		return usageError(stderr, cmd, fmt.Errorf("--workers must be between 1 and %d, got %d", maxWorkers, *workers))
	}
	if len(include) == 0 {
		include = stringList{defaultInclude}
	}
	includeRE, err := compileAll("--include", include)
	if err != nil {
		return usageError(stderr, cmd, err)
	}
	excludeRE, err := compileAll("--exclude", exclude)
	if err != nil {
		return usageError(stderr, cmd, err)
	}
	log, err := common.logger(stderr)
	if err != nil {
		return usageError(stderr, cmd, err)
	}
	engine, err := common.engine(environ)
	if err != nil {
		return usageError(stderr, cmd, err)
	}

	opts := tree.Options{
		Include:     includeRE,
		Exclude:     excludeRE,
		Workers:     *workers,
		MaxFileSize: common.maxFileSize,
		Logger:      log,
	}
	start := time.Now()
	var report tree.Report
	if cmd == "render" {
		report, err = tree.Apply(ctx, *dir, engine, opts)
	} else {
		report, err = tree.Check(ctx, *dir, engine, opts)
	}

	for _, p := range report.Problems {
		fmt.Fprintf(stderr, "%s:%d:%d: %s\n", filepath.Join(*dir, filepath.FromSlash(p.Path)), p.Line, p.Column, p.Message(engine.Syntax()))
	}
	if report.Truncated {
		fmt.Fprintf(stderr, "charmap %s: a file has more than %d problems; the rest are not shown\n", cmd, token.MaxProblems)
	}
	if err != nil {
		fmt.Fprintf(stderr, "charmap %s: %v\n", cmd, err)
	}
	log.Info(cmd+" finished",
		slog.String("dir", *dir),
		slog.Int("files", report.Files),
		slog.Int("changed", len(report.Changed)),
		slog.Int("problems", len(report.Problems)),
		slog.Duration("elapsed", time.Since(start)),
	)
	if err != nil || len(report.Problems) > 0 {
		return exitFailed
	}
	return exitOK
}

func runFilter(args, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("charmap filter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	common := addCommonFlags(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: charmap filter [flags] < in > out\n\nRenders stdin to stdout. Nothing is written to stdout unless the whole\ninput renders without a problem.\n\n")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, stderr); !ok {
		return code
	}
	if _, err := common.logger(stderr); err != nil {
		return usageError(stderr, "filter", err)
	}
	engine, err := common.engine(environ)
	if err != nil {
		return usageError(stderr, "filter", err)
	}

	data, err := io.ReadAll(io.LimitReader(stdin, common.maxFileSize+1))
	if err != nil {
		fmt.Fprintf(stderr, "charmap filter: read stdin: %v\n", err)
		return exitFailed
	}
	if int64(len(data)) > common.maxFileSize {
		fmt.Fprintf(stderr, "charmap filter: stdin is over the %d byte limit\n", common.maxFileSize)
		return exitFailed
	}
	res := engine.Render(data)
	for _, p := range res.Problems {
		fmt.Fprintf(stderr, "<stdin>:%d:%d: %s\n", p.Line, p.Column, p.Message(engine.Syntax()))
	}
	if res.Truncated {
		fmt.Fprintf(stderr, "charmap filter: more than %d problems; the rest are not shown\n", token.MaxProblems)
	}
	if len(res.Problems) > 0 {
		return exitFailed
	}
	if _, err := stdout.Write(res.Output); err != nil {
		fmt.Fprintf(stderr, "charmap filter: write stdout: %v\n", err)
		return exitFailed
	}
	return exitOK
}

// parse parses args and rejects positional arguments, which the flag package
// would otherwise stop at, silently ignoring every flag after them.
func parse(fs *flag.FlagSet, args []string, stderr io.Writer) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		// The argument is not echoed: it may be a value meant for a flag.
		fmt.Fprintf(stderr, "%s: %d unexpected positional argument(s); every option is a flag, e.g. --dir\n", fs.Name(), fs.NArg())
		return exitUsage, false
	}
	return exitOK, true
}

func usageError(stderr io.Writer, cmd string, err error) int {
	fmt.Fprintf(stderr, "charmap %s: %v\n", cmd, err)
	return exitUsage
}

func compileAll(flagName string, patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", flagName, p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func versionString() string {
	v, rev := version, ""
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" {
			v = info.Main.Version
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				rev = " " + s.Value[:min(12, len(s.Value))]
			}
		}
	}
	if v == "" {
		v = "(devel)"
	}
	return fmt.Sprintf("charmap %s%s %s %s/%s", v, rev, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}
