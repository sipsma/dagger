// Command hybrid runs each compile, assemble and link that `go build` would
// run as a Dagger exec instead, while the go command keeps planning,
// scheduling and caching.
//
//	hybrid daemon -sock S -image I -log L   holds one nested Dagger client
//	go build -toolexec 'hybrid wrap -sock S' ...
//
// The wrapper forwards compile, asm and link to the daemon and runs every
// other invocation (including the -V=full tool-ID queries) locally. For each
// action, the daemon mounts the files the action reads read-only in a
// container from the same Go image, runs the tool there, and exports the
// outputs back to the paths the go command expects.
//
// Caching is Dagger's own: the go command runs with an empty GOCACHE, so
// every action reaches the daemon, and an action whose inputs, arguments and
// environment are unchanged is a Dagger exec cache hit, which the remote cache
// can share. For that, the daemon replaces the go command's random work
// directory ($GOTMPDIR/go-buildNNN) with /work in arguments, environment and
// small text inputs, passes archives it produced on as Dagger files rather
// than re-uploading Go's rewritten copies, and uploads every other input by
// content. It is a spike: no cgo, and the work-directory numbering (bNNN)
// still ties keys to one build graph.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"dagger.io/dagger"
	"dagger.io/dagger/core"
)

type request struct {
	Tool string
	Args []string
	Dir  string
	Env  []string
}

type response struct {
	Exit   int
	Stdout string
	Stderr string
	Err    string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hybrid daemon|wrap ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "daemon":
		daemon(os.Args[2:])
	case "wrap":
		os.Exit(wrap(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", os.Args[1])
		os.Exit(2)
	}
}

// remote reports whether the daemon should run this tool invocation.
func remote(tool string, args []string) bool {
	switch filepath.Base(tool) {
	case "compile", "asm", "link":
	default:
		return false
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-V") {
			return false
		}
	}
	return true
}

func wrap(argv []string) int {
	fs := flag.NewFlagSet("wrap", flag.ExitOnError)
	sock := fs.String("sock", "", "daemon socket")
	fs.Parse(argv)
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "wrap: no tool")
		return 2
	}
	tool, args := rest[0], rest[1:]
	if !remote(tool, args) {
		cmd := exec.Command(tool, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			fmt.Fprintln(os.Stderr, "wrap:", err)
			return 2
		}
		return 0
	}
	dir, _ := os.Getwd()
	// Only what the tools read. Per-build paths such as GOCACHE and GOTMPDIR
	// would make every action's cache key differ between builds.
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GOOS", "GOARCH", "GOARM", "GOARM64", "GOAMD64", "GO386", "GOEXPERIMENT", "GOFIPS140", "GOROOT", "CGO_ENABLED", "TOOLEXEC_IMPORTPATH":
			env = append(env, kv)
		}
	}
	conn, err := net.Dial("unix", *sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wrap: dial daemon:", err)
		return 2
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Tool: tool, Args: args, Dir: dir, Env: env}); err != nil {
		fmt.Fprintln(os.Stderr, "wrap: send:", err)
		return 2
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		fmt.Fprintln(os.Stderr, "wrap: receive:", err)
		return 2
	}
	os.Stdout.WriteString(resp.Stdout)
	os.Stderr.WriteString(resp.Stderr)
	if resp.Err != "" {
		fmt.Fprintln(os.Stderr, "hybrid:", resp.Err)
		return 2
	}
	return resp.Exit
}

type uploaded struct {
	size  int64
	mtime time.Time
	file  *core.File
}

type server struct {
	q      *core.Query
	base   *core.Container
	goroot string
	work   *regexp.Regexp // the go command's work directory

	producedMu sync.Mutex
	produced   map[string]*core.File // real output path -> the exec's output

	mu    sync.Mutex
	files map[string]uploaded

	logMu sync.Mutex
	log   *json.Encoder
}

func daemon(argv []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	sock := fs.String("sock", "", "socket to listen on")
	image := fs.String("image", "", "Go image the actions run in (same toolchain as the go command)")
	logPath := fs.String("log", "", "per-action JSON lines")
	tmpdir := fs.String("gotmpdir", "", "GOTMPDIR of the go command, whose go-buildNNN work directory is replaced by /work")
	salt := fs.String("salt", "", "added to every action's environment, so separate rounds start cold")
	fs.Parse(argv)

	ctx := context.Background()
	client, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon: connect:", err)
		os.Exit(1)
	}
	defer client.Close()
	q := core.NewQuery(client)
	goroot := strings.TrimSpace(run("go", "env", "GOROOT"))
	s := &server{
		q:        q,
		base:     q.Container().From(*image).WithEnvVariable("G24_SALT", *salt),
		goroot:   goroot,
		work:     regexp.MustCompile(regexp.QuoteMeta(filepath.Clean(*tmpdir)) + `/go-build[0-9]+`),
		produced: map[string]*core.File{},
		files:    map[string]uploaded{},
	}
	lf, err := os.Create(*logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon: log:", err)
		os.Exit(1)
	}
	defer lf.Close()
	s.log = json.NewEncoder(lf)
	if _, err := s.base.Sync(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "daemon: base image:", err)
		os.Exit(1)
	}

	os.Remove(*sock)
	ln, err := net.Listen("unix", *sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon: listen:", err)
		os.Exit(1)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigs; ln.Close() }()
	fmt.Println("ready")
	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			var req request
			if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
				return
			}
			json.NewEncoder(conn).Encode(s.handle(ctx, req))
		}()
	}
	wg.Wait()
}

func run(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, name, args, err)
		os.Exit(1)
	}
	return string(out)
}

type actionLog struct {
	Tool       string  `json:"tool"`
	ImportPath string  `json:"importpath"`
	Inputs     int     `json:"inputs"`
	NewUploads int     `json:"new_uploads"`
	UploadMs   float64 `json:"upload_ms"`
	ExecMs     float64 `json:"exec_ms"`
	ExportMs   float64 `json:"export_ms"`
	TotalMs    float64 `json:"total_ms"`
	Exit       int     `json:"exit"`
	Err        string  `json:"err,omitempty"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (s *server) handle(ctx context.Context, req request) (resp response) {
	start := time.Now()
	al := actionLog{Tool: filepath.Base(req.Tool)}
	for _, kv := range req.Env {
		if v, ok := strings.CutPrefix(kv, "TOOLEXEC_IMPORTPATH="); ok {
			al.ImportPath = v
		}
	}
	defer func() {
		al.TotalMs = ms(time.Since(start))
		al.Exit = resp.Exit
		al.Err = resp.Err
		s.logMu.Lock()
		s.log.Encode(al)
		s.logMu.Unlock()
	}()

	inputs, outputs := s.files_(req)
	al.Inputs = len(inputs)
	norm := func(v string) string { return s.work.ReplaceAllString(v, "/work") }

	// Inputs: archives this daemon produced pass on as the exec's own output;
	// small files in the work directory are rewritten to the normalised path;
	// everything else is uploaded by content.
	t := time.Now()
	mounts := map[string]*core.File{}
	var others []string
	for _, p := range inputs {
		s.producedMu.Lock()
		f, ok := s.produced[p]
		s.producedMu.Unlock()
		switch {
		case ok:
			mounts[norm(p)] = f
		case s.work.MatchString(p):
			b, err := os.ReadFile(p)
			if err != nil {
				return response{Err: "read input: " + err.Error()}
			}
			name := filepath.Base(p)
			mounts[norm(p)] = s.q.Directory().WithNewFile(name, norm(string(b))).File(name)
		default:
			others = append(others, p)
		}
	}
	files, fresh, err := s.upload(ctx, others)
	al.UploadMs = ms(time.Since(t))
	al.NewUploads = fresh
	if err != nil {
		return response{Err: "upload: " + err.Error()}
	}
	for p, f := range files {
		mounts[p] = f
	}

	ctr := s.base
	for _, kv := range req.Env {
		k, v, _ := strings.Cut(kv, "=")
		ctr = ctr.WithEnvVariable(k, norm(v))
	}
	for _, out := range outputs {
		ctr = ctr.WithDirectory(filepath.Dir(norm(out)), s.q.Directory())
	}
	dir := norm(req.Dir)
	ctr = ctr.WithDirectory(dir, s.q.Directory())
	paths := make([]string, 0, len(mounts))
	for p := range mounts {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		ctr = ctr.WithMountedFile(p, mounts[p], core.ContainerWithMountedFileOpts{ReadOnly: true})
	}
	args := make([]string, 0, len(req.Args)+1)
	args = append(args, req.Tool)
	for _, a := range req.Args {
		args = append(args, norm(a))
	}
	ctr = ctr.WithWorkdir(dir).WithExec(args, core.ContainerWithExecOpts{
		DisableDaggerInDagger: true,
		Expect:                core.ReturnTypeAny,
	})

	t = time.Now()
	code, err := ctr.ExitCode(ctx)
	if err != nil {
		return response{Err: "exec: " + err.Error()}
	}
	stdout, _ := ctr.Stdout(ctx)
	stderr, _ := ctr.Stderr(ctx)
	al.ExecMs = ms(time.Since(t))
	if code != 0 {
		return response{Exit: code, Stdout: stdout, Stderr: stderr}
	}

	t = time.Now()
	var wg sync.WaitGroup
	errs := make([]error, len(outputs))
	for i, out := range outputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := ctr.File(norm(out))
			if _, errs[i] = f.Export(ctx, out); errs[i] == nil {
				s.producedMu.Lock()
				s.produced[out] = f
				s.producedMu.Unlock()
			}
		}()
	}
	wg.Wait()
	al.ExportMs = ms(time.Since(t))
	for _, err := range errs {
		if err != nil {
			return response{Err: "export: " + err.Error()}
		}
	}
	return response{Exit: 0, Stdout: stdout, Stderr: stderr}
}

// files_ returns the local files the action reads and the paths it writes.
// Inputs are every existing regular file named in its arguments (including
// response files and their contents), every file listed in an import or
// embed config, and the files of every -I directory, except files under
// GOROOT, which the action's image already has.
func (s *server) files_(req request) (inputs, outputs []string) {
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(req.Dir, p)
	}
	seen := map[string]bool{}
	add := func(p string) {
		p = abs(p)
		if seen[p] || strings.HasPrefix(p, s.goroot+"/") {
			return
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			seen[p] = true
			inputs = append(inputs, p)
		}
	}
	args := req.Args
	var expanded []string
	for _, a := range args {
		if rf, ok := strings.CutPrefix(a, "@"); ok {
			add(rf)
			if b, err := os.ReadFile(abs(rf)); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					l = strings.ReplaceAll(strings.ReplaceAll(l, `\n`, "\n"), `\\`, `\`)
					if l != "" {
						expanded = append(expanded, l)
					}
				}
			}
			continue
		}
		expanded = append(expanded, a)
	}
	for i := 0; i < len(expanded); i++ {
		a := expanded[i]
		switch a {
		case "-o", "-asmhdr":
			if i+1 < len(expanded) {
				outputs = append(outputs, abs(expanded[i+1]))
				i++
			}
			continue
		case "-importcfg":
			if i+1 < len(expanded) {
				p := abs(expanded[i+1])
				add(p)
				if b, err := os.ReadFile(p); err == nil {
					for _, l := range strings.Split(string(b), "\n") {
						if rest, ok := strings.CutPrefix(l, "packagefile "); ok {
							if _, f, ok := strings.Cut(rest, "="); ok {
								add(f)
							}
						}
					}
				}
				i++
			}
			continue
		case "-embedcfg":
			if i+1 < len(expanded) {
				p := abs(expanded[i+1])
				add(p)
				var cfg struct{ Files map[string]string }
				if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &cfg) == nil {
					for _, f := range cfg.Files {
						add(f)
					}
				}
				i++
			}
			continue
		case "-I":
			if i+1 < len(expanded) {
				d := abs(expanded[i+1])
				if ents, err := os.ReadDir(d); err == nil {
					for _, e := range ents {
						if !e.IsDir() {
							add(filepath.Join(d, e.Name()))
						}
					}
				}
				i++
			}
			continue
		}
		add(a)
	}
	return inputs, outputs
}

// upload returns a Dagger file for each input, uploading only paths that are
// new or changed since their last upload, in one host directory transfer.
func (s *server) upload(ctx context.Context, inputs []string) (map[string]*core.File, int, error) {
	out := make(map[string]*core.File, len(inputs))
	var need []string
	s.mu.Lock()
	for _, p := range inputs {
		st, err := os.Stat(p)
		if err != nil {
			s.mu.Unlock()
			return nil, 0, err
		}
		if u, ok := s.files[p]; ok && u.size == st.Size() && u.mtime.Equal(st.ModTime()) {
			out[p] = u.file
			continue
		}
		need = append(need, p)
	}
	s.mu.Unlock()
	if len(need) == 0 {
		return out, 0, nil
	}
	include := make([]string, len(need))
	for i, p := range need {
		include[i] = strings.TrimPrefix(p, "/")
	}
	dir, err := s.q.Host().Directory("/", core.HostDirectoryOpts{Include: include}).Sync(ctx)
	if err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range need {
		st, err := os.Stat(p)
		if err != nil {
			return nil, 0, err
		}
		f := dir.File(strings.TrimPrefix(p, "/"))
		s.files[p] = uploaded{size: st.Size(), mtime: st.ModTime(), file: f}
		out[p] = f
	}
	return out, len(need), nil
}
