package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agarichan/dx/internal/config"
	"github.com/agarichan/dx/internal/portless"
	"github.com/agarichan/dx/internal/registry"
	"github.com/agarichan/dx/internal/supervisor"
	"github.com/agarichan/dx/internal/worktree"
)

// ansiRE matches CSI escape sequences (color, cursor moves, line clears).
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// tsRE matches the pump's line-head capture timestamp "HH:MM:SS.mmm ".
var tsRE = regexp.MustCompile(`^(\d{2}:\d{2}:\d{2}\.\d{3}) `)

// splitTimestamp pulls a leading capture timestamp off a log line. The plain
// .log has none (returns "", line); the .color.log has one written by the pump.
func splitTimestamp(line string) (ts, rest string) {
	if m := tsRE.FindStringSubmatch(line); m != nil {
		return m[1], line[len(m[0]):]
	}
	return "", line
}

// stripANSI removes ANSI escape sequences from s.
func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// colorLogPath maps a plain log path to its sibling raw-ANSI color log path:
// "/x/api.log" -> "/x/api.color.log".
func colorLogPath(plain string) string {
	return strings.TrimSuffix(plain, ".log") + ".color.log"
}

// fileExists reports whether p exists (and is statable).
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// pumpStream copies r line by line: the color stream gets a line-head capture
// timestamp (HH:MM:SS.mmm) + the raw line; the plain stream gets the
// ANSI-stripped line with NO timestamp (kept pristine for AI/grep direct reads).
//
// bufio.Reader (not Scanner) so an arbitrarily long line never hits a size
// cap — a capped Scanner would stop reading on a >cap line, fill the child's
// pipe, and hang the child + freeze the log. ReadString keeps the trailing
// "\n", so use Fprint (not Fprintln) to avoid doubling/fabricating it.
func pumpStream(r io.Reader, plain, color io.Writer, now func() time.Time) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			fmt.Fprintf(color, "%s %s", now().Format("15:04:05.000"), line)
			fmt.Fprint(plain, stripANSI(line))
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// runLogPump is the hidden `dx __logpump` subcommand. It runs detached as the
// session leader, forks the real child, and fans the child's combined
// stdout+stderr out to two files: the ANSI-stripped plain canonical log and the
// raw-ANSI color log. It writes only to files (no stdout/stderr writers).
func runLogPump(args []string) int {
	fs := flag.NewFlagSet("__logpump", flag.ContinueOnError)
	plainPath := fs.String("plain", "", "plain (ANSI-stripped) log path")
	colorPath := fs.String("color", "", "raw-ANSI color log path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	child := fs.Args()
	if *plainPath == "" || *colorPath == "" || len(child) == 0 {
		return 2
	}
	plainF, err := os.OpenFile(*plainPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 1
	}
	defer plainF.Close()
	colorF, err := os.OpenFile(*colorPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 1
	}
	defer colorF.Close()
	cmd := exec.Command(child[0], child[1:]...)
	pr, err := cmd.StdoutPipe()
	if err != nil {
		return 1
	}
	cmd.Stderr = cmd.Stdout // combine; set after StdoutPipe so stderr shares the pipe
	if err := cmd.Start(); err != nil {
		return 1
	}
	_ = pumpStream(pr, plainF, colorF, time.Now)
	if err := cmd.Wait(); err != nil {
		return 1
	}
	return 0
}

func openReg() (*registry.Registry, error) {
	wt, err := worktree.Detect(".", gitRun("."))
	if err != nil {
		return nil, fmt.Errorf("detect worktree: %w", err)
	}
	reg, err := registry.Open(registry.StateRoot(os.Getenv), wt.Toplevel)
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}
	return reg, nil
}

func downRegistry(reg *registry.Registry, stdout, stderr io.Writer) int {
	list, err := reg.List()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, s := range list {
		if supervisor.Alive(s.PID) {
			if err := supervisor.StopGroup(s.PID); err != nil {
				fmt.Fprintln(stderr, err)
			} else {
				fmt.Fprintf(stdout, "stopped %s (pid %d)\n", s.Name, s.PID)
			}
		}
		_ = reg.Remove(s.Name)
	}
	return 0
}

func runDown(stdout, stderr io.Writer) int {
	reg, err := openReg()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return downRegistry(reg, stdout, stderr)
}

// sshBin resolves the ssh binary: PATH lookup first, /usr/bin/ssh as fallback.
// GUI-launched processes (e.g. Raycast running the dx extension) have a PATH
// without /usr/bin, so a bare "ssh" would fail with "executable not found".
func sshBin() string {
	if p, err := exec.LookPath("ssh"); err == nil {
		return p
	}
	return "/usr/bin/ssh"
}

// sshStatus fetches `dx status --all --json` from a remote host over ssh.
// The PATH prefix covers the usual install location (~/.local/bin), which
// non-interactive ssh shells typically do not have on PATH.
// Package var so tests can fake the ssh hop.
var sshStatus = func(host string) ([]byte, error) {
	cmd := exec.Command(sshBin(), "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host, "--",
		`PATH="$HOME/.local/bin:$PATH" dx status --all --json`)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return out, nil
}

type statusEntry struct {
	Name  string `json:"name"`
	Root  string `json:"root"`
	State string `json:"state"`
	PID   int    `json:"pid"`
	URL   string `json:"url"`
	Log   string `json:"log"`
	Key   string `json:"key"`
	Open  bool   `json:"open"`
	Host  string `json:"host,omitempty"` // ssh host the service runs on; empty = local
}

// remoteEntries fetches and tags one remote host's services.
func remoteEntries(host string) ([]statusEntry, error) {
	out, err := sshStatus(host)
	if err != nil {
		return nil, err
	}
	var entries []statusEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse remote status: %w", err)
	}
	for i := range entries {
		entries[i].Host = host
	}
	return entries, nil
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	all, asJSON := false, false
	var hosts []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--all" || a == "-a":
			all = true
		case a == "--json":
			asJSON = true
		case a == "--host":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "usage: dx status [--all] [--json] [--host <ssh-host>]...")
				return 2
			}
			i++
			hosts = append(hosts, args[i])
		case strings.HasPrefix(a, "--host="):
			hosts = append(hosts, strings.TrimPrefix(a, "--host="))
		}
	}

	var located []registry.Located
	if all {
		ls, err := registry.All(registry.StateRoot(os.Getenv))
		if err != nil {
			fmt.Fprintln(stderr, fmt.Errorf("list services: %w", err))
			return 1
		}
		located = ls
	} else {
		reg, err := openReg()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		list, err := reg.List()
		if err != nil {
			fmt.Fprintln(stderr, fmt.Errorf("list services: %w", err))
			return 1
		}
		for _, s := range list {
			located = append(located, registry.Located{Service: s, Registry: reg})
		}
	}

	entries := make([]statusEntry, 0, len(located))
	for _, l := range located {
		state := "stopped"
		if supervisor.Alive(l.Service.PID) {
			state = "running"
		}
		entries = append(entries, statusEntry{
			Name: l.Service.Name, Root: l.Service.Root, State: state,
			PID: l.Service.PID, URL: l.Service.URL, Log: l.Service.LogPath,
			Key: l.Service.Key, Open: l.Service.Open,
		})
	}

	// Remote hosts: a failing host is a warning, not a failure — local (and
	// other hosts') results still come back, so e.g. the Raycast list keeps
	// working when one machine is asleep.
	for _, h := range hosts {
		re, err := remoteEntries(h)
		if err != nil {
			fmt.Fprintf(stderr, "warning: host %s: %v\n", h, err)
			continue
		}
		entries = append(entries, re...)
	}

	if asJSON {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}

	if len(entries) == 0 {
		if all {
			fmt.Fprintln(stdout, "no services running")
		} else {
			fmt.Fprintln(stdout, "no services registered for this checkout")
		}
		return 0
	}

	if all {
		// group by (host, root); remote roots are prefixed "host:"
		seen := map[string]bool{}
		for _, e := range entries {
			key := e.Host + "\x00" + e.Root
			if seen[key] {
				continue
			}
			seen[key] = true
			root := e.Root
			if root == "" {
				root = "(unknown checkout)"
			}
			if e.Host != "" {
				root = e.Host + ":" + root
			}
			fmt.Fprintln(stdout, root)
			for _, f := range entries {
				if f.Host == e.Host && f.Root == e.Root {
					fmt.Fprintf(stdout, "  %-26s %-8s pid=%-7d %s\n", f.Name, f.State, f.PID, f.URL)
				}
			}
		}
		return 0
	}

	for _, e := range entries {
		fmt.Fprintf(stdout, "%-26s %-8s pid=%-7d %s\n", e.Name, e.State, e.PID, e.Log)
	}
	return 0
}

var logColors = []string{"\033[36m", "\033[35m", "\033[32m", "\033[33m", "\033[34m", "\033[31m"}

const logReset = "\033[0m"
const logTSColor = "\033[90m" // gray; uniform across services

// colorFor returns the ANSI color for service index i, or "" when disabled.
func colorFor(i int, enabled bool) string {
	if !enabled {
		return ""
	}
	return logColors[i%len(logColors)]
}

// prefixLine formats "<name padded> [ts] | <line>".
// The name is wrapped in color when color != ""; the timestamp (if present) is
// always wrapped in logTSColor (uniform gray) rather than the per-service color.
func prefixLine(name string, width int, color, ts, line string) string {
	namePart := fmt.Sprintf("%-*s", width, name)
	if color != "" {
		namePart = color + namePart + logReset
	}
	label := namePart
	if ts != "" {
		if color != "" {
			label += " " + logTSColor + ts + logReset
		} else {
			label += " " + ts
		}
	}
	label += " |"
	return label + " " + line
}

// isTerminal reports whether w is a character device (TTY).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runLogs(args []string, stdout, stderr io.Writer) int {
	follow, noColor, showTS := false, false, false
	filter := ""
	for _, a := range args {
		switch a {
		case "-f", "--follow":
			follow = true
		case "--no-color", "--plain":
			noColor = true
		case "-t", "--time":
			showTS = true
		default:
			if !strings.HasPrefix(a, "-") {
				filter = a
			}
		}
	}
	reg, err := openReg()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	list, err := reg.List()
	if err != nil {
		fmt.Fprintln(stderr, fmt.Errorf("list services: %w", err))
		return 1
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	// Resolve filter as a config key → portless name, so `dx logs api` works.
	if filter != "" {
		if cfg, wt, cerr := loadConfig(); cerr == nil {
			if svc, ok := cfg.Service(filter); ok {
				filter = portless.SvcName(svc.Name, wt.Branch, wt.IsPrimary)
			}
		}
	}
	if filter != "" {
		var f []registry.Service
		for _, s := range list {
			if s.Name == filter {
				f = append(f, s)
			}
		}
		list = f
	}
	if len(list) == 0 {
		fmt.Fprintln(stderr, "no services to tail")
		return 1
	}
	colorEnabled := !noColor && isTerminal(stdout)
	width := 0
	for _, s := range list {
		if len(s.Name) > width {
			width = len(s.Name)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var cmds []*exec.Cmd
	for i, s := range list {
		color := colorFor(i, colorEnabled)
		// Pick the file: prefer the raw-ANSI .color.log when color is enabled OR
		// when -t/--time is requested (it needs the capture timestamp that only
		// lives in the color log); otherwise read the plain canonical .log.
		logFile := s.LogPath
		if colorEnabled || showTS {
			if cf := colorLogPath(s.LogPath); fileExists(cf) {
				logFile = cf
			}
		}
		tailArgs := []string{"-n", "10"}
		if follow {
			tailArgs = append(tailArgs, "-F")
		}
		tailArgs = append(tailArgs, logFile)
		cmd := exec.Command("tail", tailArgs...)
		pipe, perr := cmd.StdoutPipe()
		if perr != nil {
			fmt.Fprintln(stderr, perr)
			continue
		}
		cmd.Stderr = stderr
		if serr := cmd.Start(); serr != nil {
			fmt.Fprintln(stderr, serr)
			continue
		}
		cmds = append(cmds, cmd)
		wg.Add(1)
		go func(name, color string, r io.Reader) {
			defer wg.Done()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				ts, rest := splitTimestamp(sc.Text())
				if color == "" { // color disabled: strip ANSI (we may be reading .color.log just for -t)
					rest = stripANSI(rest)
				}
				if !showTS {
					ts = ""
				}
				mu.Lock()
				fmt.Fprintln(stdout, prefixLine(name, width, color, ts, rest))
				mu.Unlock()
			}
		}(s.Name, color, pipe)
	}
	wg.Wait()
	for _, c := range cmds {
		_ = c.Wait()
	}
	return 0
}

func runStop(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(stderr, "usage: dx stop <name>")
		return 2
	}
	name := args[0]
	// Allow a dx.toml service key (e.g. `dx stop api`) — resolve to the portless
	// name for the current checkout. Best-effort: unknown keys fall through literally.
	if cfg, wt, cerr := loadConfig(); cerr == nil {
		if svc, ok := cfg.Service(name); ok {
			name = portless.SvcName(svc.Name, wt.Branch, wt.IsPrimary)
		}
	}
	all, err := registry.All(registry.StateRoot(os.Getenv))
	if err != nil {
		fmt.Fprintln(stderr, fmt.Errorf("list services: %w", err))
		return 1
	}
	for _, l := range all {
		if l.Service.Name != name {
			continue
		}
		if supervisor.Alive(l.Service.PID) {
			if err := supervisor.StopGroup(l.Service.PID); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		_ = l.Registry.Remove(name)
		fmt.Fprintf(stdout, "stopped %s (pid %d)\n", name, l.Service.PID)
		return 0
	}
	fmt.Fprintf(stderr, "no such service: %s\n", name)
	return 1
}

// runUp starts every service declared in dx.toml (idempotent per service).
func runUp(stdout, stderr io.Writer) int {
	cfg, wt, err := loadConfig()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(cfg.Services) == 0 {
		fmt.Fprintln(stderr, "no [service.<key>] declared in dx.toml")
		return 1
	}
	pcli := portless.Client{R: config.Load(os.Getenv)}
	reg, err := registry.Open(registry.StateRoot(os.Getenv), wt.Toplevel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rc := 0
	for _, k := range cfg.ServiceKeys() {
		svc := cfg.Services[k]
		if err := startService(cfg, svc, wt, reg, pcli, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			rc = 1
		}
	}
	return rc
}
