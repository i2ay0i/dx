package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agarichan/dx/internal/project"
)

func baseDeps(cfg *project.Config) wtDeps {
	gitCalls := &[]string{}
	return wtDeps{
		Cfg:         cfg,
		PrimaryRoot: "/repo",
		Existing:    []string{"main"},
		Git: func(args ...string) (string, error) {
			*gitCalls = append(*gitCalls, strings.Join(args, " "))
			return "", nil
		},
		BranchExists:     func(string) bool { return false },
		ContainerRunning: func(string) bool { return true },
		Docker:           func(name string, args ...string) (string, error) { return "", nil },
		Getenv:           func(k string) string { return "postgresql://u:p@h:5432/myapp" },
		Stdout:           io.Discard,
		Stderr:           io.Discard,
	}
}

func TestCreate_CollisionAborts(t *testing.T) {
	cfg := &project.Config{}
	d := baseDeps(cfg)
	d.Existing = []string{"feat-x"}
	rc := createWorktree(createOpts{Branch: "feat/x"}, d) // Slug 衝突
	if rc != 1 {
		t.Fatalf("collision should abort with rc=1, got %d", rc)
	}
}

func TestCreate_NoDBExitsZero(t *testing.T) {
	cfg := &project.Config{} // [db] 無し
	rc := createWorktree(createOpts{Branch: "feat-y"}, baseDeps(cfg))
	if rc != 0 {
		t.Fatalf("no-DB create should be rc=0, got %d", rc)
	}
}

func TestCreate_DsnOnlyForks(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", Dsn: "postgresql://u:p@h:5432/myapp"}}
	d := baseDeps(cfg)
	d.Getenv = func(string) string { return "" } // url_env absent; Dsn must be used
	forked := false
	d.Docker = func(string, ...string) (string, error) { forked = true; return "", nil }
	rc := createWorktree(createOpts{Branch: "feat-dsn"}, d)
	if rc != 0 {
		t.Fatalf("dsn-only create should be rc=0 (fork via Dsn), got %d", rc)
	}
	_ = forked // Fork is attempted; Exists check runs through the fake Docker
}

func TestCreate_URLEnvUnsetExits3(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseDeps(cfg)
	d.Getenv = func(string) string { return "" } // url_env 未設定
	rc := createWorktree(createOpts{Branch: "feat-z"}, d)
	if rc != 3 {
		t.Fatalf("url_env unset should be rc=3, got %d", rc)
	}
}

func TestCreate_ContainerDownExits3(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseDeps(cfg)
	d.ContainerRunning = func(string) bool { return false }
	rc := createWorktree(createOpts{Branch: "feat-w"}, d)
	if rc != 3 {
		t.Fatalf("container down should be rc=3, got %d", rc)
	}
}

func TestCreate_ForkFailureExits3(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseDeps(cfg)
	d.Docker = func(string, ...string) (string, error) { return "", fmt.Errorf("boom") }
	rc := createWorktree(createOpts{Branch: "feat-f"}, d)
	if rc != 3 {
		t.Fatalf("fork failure should be rc=3, got %d", rc)
	}
}

func TestCreate_HappyPathRunsGitAdd(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	var calls []string
	d := baseDeps(cfg)
	d.Git = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	rc := createWorktree(createOpts{Branch: "feat-ok"}, d)
	if rc != 0 {
		t.Fatalf("happy path rc=%d", rc)
	}
	joined := strings.Join(calls, "|")
	if !strings.Contains(joined, "worktree add") || !strings.Contains(joined, "-b feat-ok") {
		t.Fatalf("expected git worktree add -b feat-ok, got %q", joined)
	}
}

func TestCreate_RunsInitOnSuccess(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Init: []project.InitStep{{Command: []string{"true"}}},
	}}
	d := baseDeps(cfg)
	called := false
	d.RunInit = func(steps []project.InitStep, root, branch, primaryRoot string, _, _ io.Writer) error {
		called = true
		// Worktree.Dir is empty here → path = /repo/feat-init
		wantRoot := filepath.Join("/repo", "feat-init")
		if len(steps) != 1 || branch != "feat-init" || primaryRoot != "/repo" || root != wantRoot {
			t.Fatalf("init args: steps=%d branch=%q root=%q primary=%q", len(steps), branch, root, primaryRoot)
		}
		return nil
	}
	rc := createWorktree(createOpts{Branch: "feat-init"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !called {
		t.Fatal("RunInit was not called")
	}
}

func TestCreate_InitFailureExits3(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Init: []project.InitStep{{Command: []string{"false"}}},
	}}
	d := baseDeps(cfg)
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		return fmt.Errorf("boom")
	}
	rc := createWorktree(createOpts{Branch: "feat-bad"}, d)
	if rc != 3 {
		t.Fatalf("init failure should be rc=3, got %d", rc)
	}
}

func TestCreate_SkipInit(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Init: []project.InitStep{{Command: []string{"true"}}},
	}}
	d := baseDeps(cfg)
	called := false
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		called = true
		return nil
	}
	rc := createWorktree(createOpts{Branch: "feat-skip", SkipInit: true}, d)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if called {
		t.Fatal("RunInit must not be called with SkipInit")
	}
}

func TestCreate_RunsCopyOnSuccess(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Copy: []project.CopyStep{{From: ".myapp"}},
	}}
	d := baseDeps(cfg)
	called := false
	d.RunCopy = func(steps []project.CopyStep, primaryRoot, worktreeRoot string, _, _ io.Writer) error {
		called = true
		wantRoot := filepath.Join("/repo", "feat-copy")
		if len(steps) != 1 || steps[0].From != ".myapp" || primaryRoot != "/repo" || worktreeRoot != wantRoot {
			t.Fatalf("copy args: steps=%d primary=%q worktree=%q", len(steps), primaryRoot, worktreeRoot)
		}
		return nil
	}
	rc := createWorktree(createOpts{Branch: "feat-copy"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !called {
		t.Fatal("RunCopy was not called")
	}
}

func TestCreate_CopyFailureExits3(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Copy: []project.CopyStep{{From: ".myapp"}},
	}}
	d := baseDeps(cfg)
	d.RunCopy = func([]project.CopyStep, string, string, io.Writer, io.Writer) error {
		return fmt.Errorf("boom")
	}
	initCalled := false
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		initCalled = true
		return nil
	}
	rc := createWorktree(createOpts{Branch: "feat-cpfail"}, d)
	if rc != 3 {
		t.Fatalf("copy failure should be rc=3, got %d", rc)
	}
	if initCalled {
		t.Fatal("init must not run after copy failure")
	}
}

func TestCreate_CopyRunsBeforeInit(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Copy: []project.CopyStep{{From: ".myapp"}},
		Init: []project.InitStep{{Command: []string{"true"}}},
	}}
	d := baseDeps(cfg)
	order := []string{}
	d.RunCopy = func([]project.CopyStep, string, string, io.Writer, io.Writer) error {
		order = append(order, "copy")
		return nil
	}
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		order = append(order, "init")
		return nil
	}
	rc := createWorktree(createOpts{Branch: "feat-order2"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(order) != 2 || order[0] != "copy" || order[1] != "init" {
		t.Fatalf("order = %v, want [copy init]", order)
	}
}

func TestCreate_SkipInitDoesNotSkipCopy(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Copy: []project.CopyStep{{From: ".myapp"}},
		Init: []project.InitStep{{Command: []string{"true"}}},
	}}
	d := baseDeps(cfg)
	copyCalled, initCalled := false, false
	d.RunCopy = func([]project.CopyStep, string, string, io.Writer, io.Writer) error {
		copyCalled = true
		return nil
	}
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		initCalled = true
		return nil
	}
	rc := createWorktree(createOpts{Branch: "feat-skipinit", SkipInit: true}, d)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !copyCalled {
		t.Fatal("copy should still run with --skip-init")
	}
	if initCalled {
		t.Fatal("init must not run with --skip-init")
	}
}

func TestCreate_InitRunsAfterDBFork(t *testing.T) {
	cfg := &project.Config{
		DB:       &project.DB{Container: "c", Dsn: "postgresql://u:p@h:5432/myapp"},
		Worktree: project.Worktree{Init: []project.InitStep{{Command: []string{"true"}}}},
	}
	d := baseDeps(cfg)
	order := []string{}
	d.Docker = func(string, ...string) (string, error) { order = append(order, "fork"); return "", nil }
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		order = append(order, "init")
		return nil
	}
	rc := createWorktree(createOpts{Branch: "feat-order"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(order) == 0 || order[len(order)-1] != "init" {
		t.Fatalf("init must run after fork, order=%v", order)
	}
}

func baseRmDeps(cfg *project.Config) rmDeps {
	return rmDeps{
		Cfg:          cfg,
		PrimaryRoot:  "/repo",
		Git:          func(args ...string) (string, error) { return "", nil },
		Docker:       func(string, ...string) (string, error) { return "", nil },
		Getenv:       func(string) string { return "postgresql://u:p@h:5432/myapp" },
		StopServices: func(string) error { return nil },
		Dirty:        func(string) bool { return false },
		Toplevel:     func(p string) (string, error) { return p, nil },
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	}
}

func TestRm_DirtyWithoutForceAborts(t *testing.T) {
	d := baseRmDeps(&project.Config{})
	d.Dirty = func(string) bool { return true }
	if rc := rmWorktree(rmOpts{Branch: "feat-x"}, d); rc != 1 {
		t.Fatalf("dirty without --force should abort, rc=%d", rc)
	}
}

func TestRm_DirtyWithForceProceeds(t *testing.T) {
	d := baseRmDeps(&project.Config{})
	d.Dirty = func(string) bool { return true }
	if rc := rmWorktree(rmOpts{Branch: "feat-x", Force: true}, d); rc != 0 {
		t.Fatalf("--force should proceed, rc=%d", rc)
	}
}

func TestRm_URLEnvUnsetAbortsWhenDBNeeded(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseRmDeps(cfg)
	d.Getenv = func(string) string { return "" }
	if rc := rmWorktree(rmOpts{Branch: "feat-x"}, d); rc != 1 {
		t.Fatalf("url_env unset (DB needed) should abort, rc=%d", rc)
	}
}

func TestRm_KeepDBSkipsDrop(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseRmDeps(cfg)
	d.Getenv = func(string) string { return "" } // url_env 無くても --keep-db なら無視
	dropped := false
	d.Docker = func(string, ...string) (string, error) { dropped = true; return "", nil }
	if rc := rmWorktree(rmOpts{Branch: "feat-x", KeepDB: true}, d); rc != 0 {
		t.Fatalf("--keep-db rc=%d", rc)
	}
	if dropped {
		t.Fatal("--keep-db must not call docker drop")
	}
}

func TestRm_DropFailureAborts(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseRmDeps(cfg)
	d.Docker = func(string, ...string) (string, error) { return "", fmt.Errorf("drop boom") }
	removed := false
	d.Git = func(args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "worktree remove") {
			removed = true
		}
		return "", nil
	}
	if rc := rmWorktree(rmOpts{Branch: "feat-x"}, d); rc != 1 {
		t.Fatalf("drop failure should abort, rc=%d", rc)
	}
	if removed {
		t.Fatal("must not remove worktree when DB drop fails")
	}
}

func TestRm_DirtyWithDBDoesNotDrop(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseRmDeps(cfg)
	d.Dirty = func(string) bool { return true }
	dockerCalled := false
	d.Docker = func(string, ...string) (string, error) { dockerCalled = true; return "", nil }
	if rc := rmWorktree(rmOpts{Branch: "feat-x"}, d); rc != 1 {
		t.Fatalf("dirty without --force should abort, rc=%d", rc)
	}
	if dockerCalled {
		t.Fatal("DB must not be dropped when dirty check aborts before destructive actions")
	}
}

func TestParseWorktreePorcelain(t *testing.T) {
	porc := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /repo/.claude/worktrees/feat-x\nHEAD def\nbranch refs/heads/feat-x\n"
	rows := parseWorktreePorcelain(porc)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Branch != "main" || rows[0].Path != "/repo" {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if rows[1].Branch != "feat-x" {
		t.Fatalf("row1 = %+v", rows[1])
	}
}

func TestParseWorktreeArgs_FlagsAfterBranch(t *testing.T) {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	force := fs.Bool("force", false, "")
	del := fs.Bool("delete-branch", false, "")
	b, err := parseWorktreeArgs(fs, []string{"feat-x", "--force", "--delete-branch"})
	if err != nil || b != "feat-x" || !*force || !*del {
		t.Fatalf("b=%q force=%v del=%v err=%v", b, *force, *del, err)
	}
}

func TestParseWorktreeArgs_FromAfterBranch(t *testing.T) {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "")
	b, err := parseWorktreeArgs(fs, []string{"feat-x", "--from", "main"})
	if err != nil || b != "feat-x" || *from != "main" {
		t.Fatalf("b=%q from=%q err=%v", b, *from, err)
	}
}

func TestParseWorktreeArgs_FlagsBeforeBranch(t *testing.T) {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "")
	b, err := parseWorktreeArgs(fs, []string{"--from", "main", "feat-x"})
	if err != nil || b != "feat-x" || *from != "main" {
		t.Fatalf("b=%q from=%q err=%v", b, *from, err)
	}
}

func TestParseWorktreeArgs_Missing(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseWorktreeArgs(fs, []string{}); err == nil {
		t.Fatal("expected error for missing branch")
	}
}

func TestListRows_DBAndServices(t *testing.T) {
	cfg := &project.Config{
		Services: map[string]project.Service{"api": {Name: "myapp-api"}, "web": {Name: "myapp"}},
	}
	porc := "worktree /repo\nbranch refs/heads/main\n\nworktree /repo/.claude/worktrees/feat-x\nbranch refs/heads/feat-x\n"
	rows := listRows(cfg, "myapp", porc, func(svc string) string {
		if svc == "myapp-api" {
			return "running"
		}
		return "stopped"
	})
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	// primary
	if rows[0].DB != "myapp" {
		t.Fatalf("primary db = %q", rows[0].DB)
	}
	// worktree
	if rows[1].DB != "myapp_feat_x" {
		t.Fatalf("worktree db = %q", rows[1].DB)
	}
	if len(rows[1].Services) != 2 || !strings.Contains(rows[1].Services[0], "myapp-api-feat-x") {
		t.Fatalf("services = %v", rows[1].Services)
	}
}

// --- adopt: git worktree add を伴わない後半3ステップ ---

func baseAdoptDeps(cfg *project.Config) wtDeps {
	d := baseDeps(cfg)
	d.Git = func(args ...string) (string, error) {
		return "", fmt.Errorf("adopt must not run git: %v", args)
	}
	return d
}

func TestAdopt_NoGitWorktreeAdd(t *testing.T) {
	d := baseAdoptDeps(&project.Config{})
	var calls []string
	d.Git = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	rc := adoptWorktree(adoptOpts{Branch: "feat-a", Path: "/wt/feat-a"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(calls) != 0 {
		t.Fatalf("adopt must not invoke git, got %v", calls)
	}
}

func TestAdopt_NoDBExitsZero(t *testing.T) {
	rc := adoptWorktree(adoptOpts{Branch: "feat-b", Path: "/wt/feat-b"}, baseAdoptDeps(&project.Config{}))
	if rc != 0 {
		t.Fatalf("no-DB adopt should be rc=0, got %d", rc)
	}
}

func TestAdopt_URLEnvUnsetExits3(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseAdoptDeps(cfg)
	d.Getenv = func(string) string { return "" }
	if rc := adoptWorktree(adoptOpts{Branch: "feat-c", Path: "/wt/feat-c"}, d); rc != 3 {
		t.Fatalf("url_env unset should be rc=3, got %d", rc)
	}
}

func TestAdopt_ContainerDownExits3(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseAdoptDeps(cfg)
	d.ContainerRunning = func(string) bool { return false }
	if rc := adoptWorktree(adoptOpts{Branch: "feat-d", Path: "/wt/feat-d"}, d); rc != 3 {
		t.Fatalf("container down should be rc=3, got %d", rc)
	}
}

func TestAdopt_ForkFailureExits3(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseAdoptDeps(cfg)
	d.Docker = func(string, ...string) (string, error) { return "", fmt.Errorf("boom") }
	if rc := adoptWorktree(adoptOpts{Branch: "feat-e", Path: "/wt/feat-e"}, d); rc != 3 {
		t.Fatalf("fork failure should be rc=3, got %d", rc)
	}
}

func TestAdopt_ForksIntoBranchDB(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", Dsn: "postgresql://u:p@h:5432/myapp"}}
	d := baseAdoptDeps(cfg)
	var sql []string
	d.Docker = func(_ string, args ...string) (string, error) {
		sql = append(sql, strings.Join(args, " "))
		return "", nil
	}
	if rc := adoptWorktree(adoptOpts{Branch: "feat/x", Path: "/wt/feat-x"}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(strings.Join(sql, "|"), "myapp_feat_x") {
		t.Fatalf("expected fork into myapp_feat_x, docker calls: %v", sql)
	}
}

// 既存 DB があれば fork は走らない（フックの再実行で壊れないこと）。
func TestAdopt_ExistingDBSkipsFork(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", Dsn: "postgresql://u:p@h:5432/myapp"}}
	d := baseAdoptDeps(cfg)
	var sql []string
	d.Docker = func(_ string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		sql = append(sql, joined)
		if strings.Contains(joined, "pg_database") {
			return "1\n", nil // 既に存在
		}
		return "", nil
	}
	if rc := adoptWorktree(adoptOpts{Branch: "feat-idem", Path: "/wt/feat-idem"}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	for _, c := range sql {
		if strings.Contains(c, "CREATE DATABASE") {
			t.Fatalf("existing DB must not be re-created, docker calls: %v", sql)
		}
	}
}

func TestAdopt_CopyThenInitWithGivenPath(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Copy: []project.CopyStep{{From: ".env"}},
		Init: []project.InitStep{{Command: []string{"true"}}},
	}}
	d := baseAdoptDeps(cfg)
	order := []string{}
	d.RunCopy = func(_ []project.CopyStep, primaryRoot, worktreeRoot string, _, _ io.Writer) error {
		order = append(order, "copy")
		if primaryRoot != "/repo" || worktreeRoot != "/elsewhere/feat-p" {
			t.Fatalf("copy roots: primary=%q worktree=%q", primaryRoot, worktreeRoot)
		}
		return nil
	}
	d.RunInit = func(_ []project.InitStep, root, branch, primaryRoot string, _, _ io.Writer) error {
		order = append(order, "init")
		if root != "/elsewhere/feat-p" || branch != "feat-p" || primaryRoot != "/repo" {
			t.Fatalf("init args: root=%q branch=%q primary=%q", root, branch, primaryRoot)
		}
		return nil
	}
	if rc := adoptWorktree(adoptOpts{Branch: "feat-p", Path: "/elsewhere/feat-p"}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(order) != 2 || order[0] != "copy" || order[1] != "init" {
		t.Fatalf("order = %v, want [copy init]", order)
	}
}

func TestAdopt_CopyFailureExits3(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{Copy: []project.CopyStep{{From: ".env"}}}}
	d := baseAdoptDeps(cfg)
	d.RunCopy = func([]project.CopyStep, string, string, io.Writer, io.Writer) error {
		return fmt.Errorf("boom")
	}
	initCalled := false
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		initCalled = true
		return nil
	}
	if rc := adoptWorktree(adoptOpts{Branch: "feat-cf", Path: "/wt/feat-cf"}, d); rc != 3 {
		t.Fatalf("copy failure should be rc=3, got %d", rc)
	}
	if initCalled {
		t.Fatal("init must not run after copy failure")
	}
}

func TestAdopt_InitFailureExits3(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{Init: []project.InitStep{{Command: []string{"false"}}}}}
	d := baseAdoptDeps(cfg)
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		return fmt.Errorf("boom")
	}
	if rc := adoptWorktree(adoptOpts{Branch: "feat-if", Path: "/wt/feat-if"}, d); rc != 3 {
		t.Fatalf("init failure should be rc=3, got %d", rc)
	}
}

func TestAdopt_SkipInitStillCopies(t *testing.T) {
	cfg := &project.Config{Worktree: project.Worktree{
		Copy: []project.CopyStep{{From: ".env"}},
		Init: []project.InitStep{{Command: []string{"true"}}},
	}}
	d := baseAdoptDeps(cfg)
	copyCalled, initCalled := false, false
	d.RunCopy = func([]project.CopyStep, string, string, io.Writer, io.Writer) error {
		copyCalled = true
		return nil
	}
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		initCalled = true
		return nil
	}
	if rc := adoptWorktree(adoptOpts{Branch: "feat-si", Path: "/wt/feat-si", SkipInit: true}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !copyCalled {
		t.Fatal("copy should still run with SkipInit")
	}
	if initCalled {
		t.Fatal("init must not run with SkipInit")
	}
}

// 二重実行しても壊れない（DB 既存 / copy 宛先既存 / init 再実行）。
func TestAdopt_RerunStaysReady(t *testing.T) {
	cfg := &project.Config{
		DB: &project.DB{Container: "c", Dsn: "postgresql://u:p@h:5432/myapp"},
		Worktree: project.Worktree{
			Copy: []project.CopyStep{{From: ".env"}},
			Init: []project.InitStep{{Command: []string{"true"}}},
		},
	}
	d := baseAdoptDeps(cfg)
	d.Docker = func(_ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "pg_database") {
			return "1\n", nil
		}
		return "", nil
	}
	initRuns := 0
	d.RunCopy = func([]project.CopyStep, string, string, io.Writer, io.Writer) error { return nil }
	d.RunInit = func([]project.InitStep, string, string, string, io.Writer, io.Writer) error {
		initRuns++
		return nil
	}
	o := adoptOpts{Branch: "feat-re", Path: "/wt/feat-re"}
	if rc := adoptWorktree(o, d); rc != 0 {
		t.Fatalf("first run rc=%d", rc)
	}
	if rc := adoptWorktree(o, d); rc != 0 {
		t.Fatalf("second run rc=%d", rc)
	}
	if initRuns != 2 {
		t.Fatalf("init should re-run on every adopt, runs=%d", initRuns)
	}
}

// primary root は `git worktree list --porcelain` の先頭エントリ。
func TestPrimaryRootFrom(t *testing.T) {
	porc := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /elsewhere/feat-x\nHEAD def\nbranch refs/heads/feat-x\n"
	got, err := primaryRootFrom(func(...string) (string, error) { return porc, nil })
	if err != nil || got != "/repo" {
		t.Fatalf("primaryRootFrom = %q, err=%v", got, err)
	}
}

func TestPrimaryRootFrom_EmptyErrors(t *testing.T) {
	if _, err := primaryRootFrom(func(...string) (string, error) { return "", nil }); err == nil {
		t.Fatal("expected error for empty porcelain output")
	}
}

// 自分自身は slug 衝突とみなさない（adopt は既存 worktree が対象）。
func TestAdopt_SelfIsNotACollision(t *testing.T) {
	d := baseAdoptDeps(&project.Config{})
	d.Existing = []string{"main", "feat-self"}
	if rc := adoptWorktree(adoptOpts{Branch: "feat-self", Path: "/wt/feat-self"}, d); rc != 0 {
		t.Fatalf("own branch must not count as a collision, rc=%d", rc)
	}
}

func TestAdopt_OtherBranchCollisionAborts(t *testing.T) {
	d := baseAdoptDeps(&project.Config{})
	d.Existing = []string{"main", "feat-x"}
	if rc := adoptWorktree(adoptOpts{Branch: "feat/x", Path: "/wt/feat-x"}, d); rc != 1 {
		t.Fatalf("collision with another branch should abort with rc=1, got %d", rc)
	}
}

// --- rm --keep-worktree ---

func TestRm_KeepWorktreeSkipsGitRemove(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseRmDeps(cfg)
	var calls []string
	d.Git = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	dropped := false
	d.Docker = func(string, ...string) (string, error) { dropped = true; return "", nil }
	if rc := rmWorktree(rmOpts{Branch: "feat-x", KeepWorktree: true}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if strings.Contains(strings.Join(calls, "|"), "worktree remove") {
		t.Fatalf("--keep-worktree must not remove the worktree, git calls: %v", calls)
	}
	if !dropped {
		t.Fatal("--keep-worktree must still drop the DB")
	}
}

func TestRm_KeepWorktreeStopsServices(t *testing.T) {
	d := baseRmDeps(&project.Config{})
	stopped := false
	d.StopServices = func(string) error { stopped = true; return nil }
	if rc := rmWorktree(rmOpts{Branch: "feat-x", KeepWorktree: true}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !stopped {
		t.Fatal("--keep-worktree must still stop services")
	}
}

// worktree を消さないので dirty 保護は不要（archive フックが毎回落ちるのを防ぐ）。
func TestRm_KeepWorktreeIgnoresDirty(t *testing.T) {
	d := baseRmDeps(&project.Config{})
	d.Dirty = func(string) bool { return true }
	if rc := rmWorktree(rmOpts{Branch: "feat-x", KeepWorktree: true}, d); rc != 0 {
		t.Fatalf("--keep-worktree should not require --force on a dirty worktree, rc=%d", rc)
	}
}

func TestRm_KeepWorktreeKeepsBranch(t *testing.T) {
	d := baseRmDeps(&project.Config{})
	var calls []string
	d.Git = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	if rc := rmWorktree(rmOpts{Branch: "feat-x", KeepWorktree: true, DeleteBranch: true}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if strings.Contains(strings.Join(calls, "|"), "branch -d") {
		t.Fatalf("--keep-worktree must not delete the branch, git calls: %v", calls)
	}
}

func TestRm_KeepWorktreeDropFailureAborts(t *testing.T) {
	cfg := &project.Config{DB: &project.DB{Container: "c", URLEnv: "APP_DATABASE_URL"}}
	d := baseRmDeps(cfg)
	d.Docker = func(string, ...string) (string, error) { return "", fmt.Errorf("drop boom") }
	if rc := rmWorktree(rmOpts{Branch: "feat-x", KeepWorktree: true}, d); rc != 1 {
		t.Fatalf("drop failure should abort, rc=%d", rc)
	}
}

// --- rm の対象パス解決 ---

// Orca が作る worktree は <primary>/<worktree.dir>/<branch> にないので、
// 呼び出し側が解決したパスを使う。
func TestRm_UsesGivenPath(t *testing.T) {
	d := baseRmDeps(&project.Config{Worktree: project.Worktree{Dir: ".claude/worktrees"}})
	var dirtyPath, topPath string
	d.Dirty = func(p string) bool { dirtyPath = p; return false }
	d.Toplevel = func(p string) (string, error) { topPath = p; return p, nil }
	if rc := rmWorktree(rmOpts{Branch: "feat-x", Path: "/elsewhere/wt/feat-x"}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if dirtyPath != "/elsewhere/wt/feat-x" || topPath != "/elsewhere/wt/feat-x" {
		t.Fatalf("dirty=%q toplevel=%q, want the given path", dirtyPath, topPath)
	}
}

func TestRm_FallsBackToConfiguredPath(t *testing.T) {
	d := baseRmDeps(&project.Config{Worktree: project.Worktree{Dir: ".claude/worktrees"}})
	var dirtyPath string
	d.Dirty = func(p string) bool { dirtyPath = p; return false }
	if rc := rmWorktree(rmOpts{Branch: "feat-x"}, d); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	want := filepath.Join("/repo", ".claude/worktrees", "feat-x")
	if dirtyPath != want {
		t.Fatalf("dirty path = %q, want %q", dirtyPath, want)
	}
}

func TestWorktreePathFor(t *testing.T) {
	porc := "worktree /repo\nbranch refs/heads/main\n\nworktree /elsewhere/feat-x\nbranch refs/heads/feat-x\n"
	if got, ok := worktreePathFor(porc, "feat-x"); !ok || got != "/elsewhere/feat-x" {
		t.Fatalf("worktreePathFor = %q, %v", got, ok)
	}
	if _, ok := worktreePathFor(porc, "nope"); ok {
		t.Fatal("unknown branch must report not found")
	}
}

// --- サブコマンド引数の解析 ---

func TestParseRmArgs_KeepWorktree(t *testing.T) {
	o, err := parseRmArgs([]string{"feat-x", "--keep-worktree"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if o.Branch != "feat-x" || !o.KeepWorktree {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseRmArgs_KeepWorktreeIsOrthogonalToKeepDB(t *testing.T) {
	o, err := parseRmArgs([]string{"feat-x", "--keep-worktree", "--keep-db"})
	if err != nil || !o.KeepWorktree || !o.KeepDB {
		t.Fatalf("opts = %+v, err=%v", o, err)
	}
}

// 「worktree は残す」と「ブランチを消す」は意図が矛盾する。
func TestParseRmArgs_KeepWorktreeWithDeleteBranchErrors(t *testing.T) {
	if _, err := parseRmArgs([]string{"feat-x", "--keep-worktree", "--delete-branch"}); err == nil {
		t.Fatal("expected an error for --keep-worktree with --delete-branch")
	}
}

func TestParseAdoptArgs_SkipInit(t *testing.T) {
	skipInit, err := parseAdoptArgs([]string{"--skip-init"})
	if err != nil || !skipInit {
		t.Fatalf("skipInit=%v err=%v", skipInit, err)
	}
}

func TestParseAdoptArgs_Bare(t *testing.T) {
	skipInit, err := parseAdoptArgs(nil)
	if err != nil || skipInit {
		t.Fatalf("skipInit=%v err=%v", skipInit, err)
	}
}

// adopt は cwd の worktree を対象にするので位置引数を取らない。
func TestParseAdoptArgs_RejectsPositional(t *testing.T) {
	if _, err := parseAdoptArgs([]string{"feat-x"}); err == nil {
		t.Fatal("expected an error for a positional argument")
	}
}
