package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agarichan/dx/internal/registry"
	"github.com/agarichan/dx/internal/supervisor"
)

func TestRunUp_NoConfig(t *testing.T) {
	// loadConfig は cwd の git/dx.toml に依存するため、ここでは
	// project.Load 不在時に runUp が非0を返すことを確認する統合寄りテスト。
	// CI 非依存にするため、dx.toml の無い一時ディレクトリへ chdir して検証。
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var out, errb strings.Builder
	if rc := runUp(&out, &errb); rc == 0 {
		t.Fatalf("expected non-zero rc outside a configured repo, got 0 (err=%q)", errb.String())
	}
}

func TestDown_StopsRegistered(t *testing.T) {
	// arrange: start a sleeper and register it under a temp state root + cwd repo
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo := t.TempDir()
	reg, err := registry.Open(registry.StateRoot(os.Getenv), repo)
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(reg.Dir, "x.log")
	pid, err := supervisor.Start(supervisor.Spec{Command: []string{"/bin/sh", "-c", "sleep 30"}, LogPath: log})
	if err != nil {
		t.Fatal(err)
	}
	reg.Put(registry.Service{Name: "x", PID: pid, LogPath: log})

	// act: downAll on this registry
	var out, errb bytes.Buffer
	if code := downRegistry(reg, &out, &errb); code != 0 {
		t.Fatalf("down code=%d err=%s", code, errb.String())
	}
	// process termination via SIGTERM is asynchronous; poll with a deadline
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && supervisor.Alive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if supervisor.Alive(pid) {
		t.Fatal("expected process stopped")
	}
	list, _ := reg.List()
	if len(list) != 0 {
		t.Fatalf("registry not cleared: %v", list)
	}
}

func TestRunStop_ByName(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	reg, _ := registry.Open(registry.StateRoot(os.Getenv), "/home/u/work/myapp")
	log := filepath.Join(reg.Dir, "s.log")
	pid, err := supervisor.Start(supervisor.Spec{Command: []string{"/bin/sh", "-c", "sleep 30"}, LogPath: log})
	if err != nil {
		t.Fatal(err)
	}
	reg.Put(registry.Service{Name: "svc-x", PID: pid, LogPath: log, Root: "/home/u/work/myapp"})

	var out, errb bytes.Buffer
	if code := runStop([]string{"svc-x"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && supervisor.Alive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if supervisor.Alive(pid) {
		t.Fatal("expected stopped")
	}
	if _, ok, _ := reg.Get("svc-x"); ok {
		t.Fatal("expected removed from registry")
	}
}

func TestRunStop_NotFound(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := runStop([]string{"nope"}, &out, &errb); code == 0 {
		t.Fatal("expected non-zero for unknown service")
	}
}

func TestRunStop_NoArg(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runStop(nil, &out, &errb); code != 2 {
		t.Fatalf("expected usage exit 2, got %d", code)
	}
}

func TestPumpStream(t *testing.T) {
	fixed := time.Date(2026, 6, 27, 12, 34, 56, 0, time.UTC)
	var plain, color bytes.Buffer
	in := "\x1b[31mhi\x1b[0m\nplain\n"
	if err := pumpStream(strings.NewReader(in), &plain, &color, func() time.Time { return fixed }); err != nil {
		t.Fatal(err)
	}
	if color.String() != "12:34:56.000 \x1b[31mhi\x1b[0m\n12:34:56.000 plain\n" {
		t.Fatalf("color = %q", color.String())
	}
	if plain.String() != "hi\nplain\n" { // no timestamp, no ANSI
		t.Fatalf("plain = %q", plain.String())
	}
}

func TestColorLogPath(t *testing.T) {
	if got := colorLogPath("/x/myapp-api.log"); got != "/x/myapp-api.color.log" {
		t.Fatalf("colorLogPath = %q", got)
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[36mINFO\x1b[0m: ready\x1b[2K"
	if got := stripANSI(in); got != "INFO: ready" {
		t.Fatalf("stripANSI = %q", got)
	}
	if stripANSI("plain text") != "plain text" {
		t.Fatal("plain text must be unchanged")
	}
}

func TestColorFor(t *testing.T) {
	if colorFor(0, false) != "" {
		t.Fatal("disabled => empty")
	}
	if colorFor(0, true) == "" {
		t.Fatal("enabled => non-empty")
	}
	if colorFor(0, true) != colorFor(len(logColors), true) {
		t.Fatal("should cycle modulo palette length")
	}
}

func TestPrefixLine_Plain(t *testing.T) {
	got := prefixLine("api", 7, "", "", "hello")
	if !strings.HasPrefix(got, "api") || !strings.HasSuffix(got, "| hello") {
		t.Fatalf("plain = %q", got)
	}
	if strings.Contains(got, "\033") {
		t.Fatalf("plain must have no ANSI: %q", got)
	}
}

func TestPrefixLine_Color(t *testing.T) {
	got := prefixLine("api", 3, "\033[36m", "", "hi")
	// Name is wrapped in per-service color; | and line are plain (outside color block).
	if !strings.HasPrefix(got, "\033[36m") {
		t.Fatalf("must start with color: %q", got)
	}
	if !strings.Contains(got, logReset) {
		t.Fatalf("must contain reset: %q", got)
	}
	if !strings.HasSuffix(got, "| hi") {
		t.Fatalf("must end with '| hi': %q", got)
	}
	if !strings.Contains(got, "|") {
		t.Fatalf("missing | separator: %q", got)
	}
}

func TestPrefixLine_TimestampUniformColor(t *testing.T) {
	got := prefixLine("myapp-api", 7, "\033[36m", "12:07:23.567", "hi")
	// Name is in service color; timestamp is in gray (logTSColor), NOT in \033[36m.
	if !strings.HasPrefix(got, "\033[36m") {
		t.Fatalf("must start with service color: %q", got)
	}
	if !strings.Contains(got, logTSColor+"12:07:23.567"+logReset) {
		t.Fatalf("timestamp must be wrapped in logTSColor: %q", got)
	}
	// Timestamp must NOT be wrapped in the per-service cyan color.
	if strings.Contains(got, "\033[36m12:07:23.567") {
		t.Fatalf("timestamp must NOT be in service color: %q", got)
	}
	if !strings.HasSuffix(got, "| hi") {
		t.Fatalf("must end with '| hi': %q", got)
	}
}

func TestPrefixLine_NoColorWithTS(t *testing.T) {
	got := prefixLine("myapp-api", 7, "", "12:07:23.567", "hi")
	want := "myapp-api 12:07:23.567 | hi"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPrefixLine_WithTimestamp(t *testing.T) {
	got := prefixLine("myapp-api", 7, "", "12:07:23.567", "hello")
	if got != "myapp-api 12:07:23.567 | hello" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitTimestamp(t *testing.T) {
	ts, rest := splitTimestamp("12:07:23.567 hello world")
	if ts != "12:07:23.567" || rest != "hello world" {
		t.Fatalf("ts=%q rest=%q", ts, rest)
	}
	if ts2, rest2 := splitTimestamp("no timestamp here"); ts2 != "" || rest2 != "no timestamp here" {
		t.Fatalf("ts2=%q rest2=%q", ts2, rest2)
	}
}

func TestIsTerminal_Buffer(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer is not a terminal")
	}
}

func TestRunStatus_AllJSON(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	// 2 つの checkout に登録（PID は実在の自分を使い running 判定させる）
	rega, _ := registry.Open(registry.StateRoot(os.Getenv), "/home/u/work/myapp")
	regb, _ := registry.Open(registry.StateRoot(os.Getenv), "/home/u/work/myapp/.claude/worktrees/x")
	rega.Put(registry.Service{Name: "myapp-api", PID: os.Getpid(), Root: "/home/u/work/myapp", URL: "https://myapp-api.dev.example.com", LogPath: "/tmp/a.log", Key: "api", Open: true})
	regb.Put(registry.Service{Name: "myapp-x", PID: 999999, Root: "/home/u/work/myapp/.claude/worktrees/x", URL: "https://myapp-x.dev.example.com", LogPath: "/tmp/b.log"})

	var out, errb bytes.Buffer
	code := runStatus([]string{"--all", "--json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d", len(entries))
	}
	byName := map[string]map[string]any{}
	for _, e := range entries {
		byName[e["name"].(string)] = e
	}
	if byName["myapp-api"]["state"] != "running" {
		t.Fatalf("myapp-api state=%v", byName["myapp-api"]["state"])
	}
	if byName["myapp-x"]["state"] != "stopped" {
		t.Fatalf("myapp-x state=%v", byName["myapp-x"]["state"])
	}
	if byName["myapp-api"]["url"] != "https://myapp-api.dev.example.com" {
		t.Fatalf("url=%v", byName["myapp-api"]["url"])
	}
	// key/open flow from the registry record into the JSON
	if byName["myapp-api"]["key"] != "api" || byName["myapp-api"]["open"] != true {
		t.Fatalf("key/open = %v/%v", byName["myapp-api"]["key"], byName["myapp-api"]["open"])
	}
	// records that predate the fields yield zero values
	if byName["myapp-x"]["key"] != "" || byName["myapp-x"]["open"] != false {
		t.Fatalf("legacy key/open = %v/%v", byName["myapp-x"]["key"], byName["myapp-x"]["open"])
	}
}

// TestRunLogPump_CombinesStreams exercises the real runLogPump (not the test
// emulation): a child writing to BOTH stdout and stderr lands in both files.
func TestRunLogPump_CombinesStreams(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "p.log")
	color := filepath.Join(dir, "c.log")
	rc := runLogPump([]string{"--plain", plain, "--color", color, "--",
		"sh", "-c", `printf 'out\n'; printf 'err\n' 1>&2`})
	if rc != 0 {
		t.Fatalf("runLogPump rc=%d", rc)
	}
	b, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Fatalf("plain log missing combined stdout+stderr: %q", got)
	}
}

// fakeSSHStatus swaps sshStatus for the duration of the test.
func fakeSSHStatus(t *testing.T, fn func(host string) ([]byte, error)) {
	t.Helper()
	orig := sshStatus
	sshStatus = fn
	t.Cleanup(func() { sshStatus = orig })
}

func TestRunStatus_RemoteHostJSON(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	reg, _ := registry.Open(registry.StateRoot(os.Getenv), "/home/u/work/myapp")
	reg.Put(registry.Service{Name: "myapp-api", PID: os.Getpid(), Root: "/home/u/work/myapp", URL: "https://myapp-api.dev.example.com"})

	fakeSSHStatus(t, func(host string) ([]byte, error) {
		if host != "nano" {
			t.Fatalf("host=%q", host)
		}
		return []byte(`[
  {"name":"remote-api","root":"/home/r/work/remote","state":"running","pid":42,"url":"https://remote-api.dev.example.com","log":"/tmp/r.log","key":"api","open":true}
]`), nil
	})

	var out, errb bytes.Buffer
	code := runStatus([]string{"--all", "--json", "--host", "nano"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d\n%s", len(entries), out.String())
	}
	byName := map[string]map[string]any{}
	for _, e := range entries {
		byName[e["name"].(string)] = e
	}
	// local entry: no host key at all (omitempty)
	if _, ok := byName["myapp-api"]["host"]; ok {
		t.Fatalf("local entry has host: %v", byName["myapp-api"])
	}
	r := byName["remote-api"]
	if r["host"] != "nano" || r["state"] != "running" || r["url"] != "https://remote-api.dev.example.com" {
		t.Fatalf("remote entry = %v", r)
	}
	if r["key"] != "api" || r["open"] != true {
		t.Fatalf("remote key/open = %v/%v", r["key"], r["open"])
	}
}

func TestRunStatus_RemoteHostEqualsForm(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fakeSSHStatus(t, func(host string) ([]byte, error) {
		return []byte(`[{"name":"remote-api","root":"/r","state":"stopped","pid":0,"url":"","log":""}]`), nil
	})
	var out, errb bytes.Buffer
	if code := runStatus([]string{"--all", "--json", "--host=nano"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"host": "nano"`) {
		t.Fatalf("missing host field:\n%s", out.String())
	}
}

func TestRunStatus_RemoteHostFailure(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	reg, _ := registry.Open(registry.StateRoot(os.Getenv), "/home/u/work/myapp")
	reg.Put(registry.Service{Name: "myapp-api", PID: os.Getpid(), Root: "/home/u/work/myapp"})

	fakeSSHStatus(t, func(host string) ([]byte, error) {
		return nil, fmt.Errorf("ssh: connect refused")
	})

	var out, errb bytes.Buffer
	code := runStatus([]string{"--all", "--json", "--host", "nano"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d (remote failure must not fail the whole status)", code)
	}
	var entries []map[string]any
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if len(entries) != 1 || entries[0]["name"] != "myapp-api" {
		t.Fatalf("local entries lost: %v", entries)
	}
	if !strings.Contains(errb.String(), "nano") {
		t.Fatalf("stderr should mention the failing host: %q", errb.String())
	}
}

func TestRunStatus_HostMissingValue(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runStatus([]string{"--all", "--host"}, &out, &errb); code != 2 {
		t.Fatalf("code=%d, want 2 (usage error)", code)
	}
}

func TestRunStatus_RemoteTextGrouping(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fakeSSHStatus(t, func(host string) ([]byte, error) {
		return []byte(`[{"name":"remote-api","root":"/home/r/work/remote","state":"running","pid":42,"url":"https://r.example.com","log":""}]`), nil
	})
	var out, errb bytes.Buffer
	if code := runStatus([]string{"--all", "--host", "nano"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "nano:/home/r/work/remote") {
		t.Fatalf("remote root header not host-prefixed:\n%s", out.String())
	}
}

func TestSSHBin_FallsBackWithoutPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ssh here
	if got := sshBin(); got != "/usr/bin/ssh" {
		t.Fatalf("sshBin()=%q, want /usr/bin/ssh when PATH has no ssh", got)
	}
}
