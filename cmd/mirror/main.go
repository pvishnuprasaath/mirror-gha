package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mirror-gha/internal/artifactserver"
	"mirror-gha/internal/cacheserver"
	"mirror-gha/internal/engine"
	"mirror-gha/internal/runner"
)

// Populated via -ldflags by GoReleaser at release build time (its default
// ldflags target exactly these variable names); "dev"/"none"/"unknown"
// are what a plain `go build` leaves them as.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "source"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("mirror %s (commit %s, built %s by %s)\n", version, commit, date, builtBy)
	case "run":
		os.Exit(runMain(os.Args[2:]))
	case "dashboard":
		fmt.Println("mirror dashboard: dashboard server not implemented yet")
	default:
		printUsage()
		os.Exit(1)
	}
}

// runMode selects which of the mutually exclusive `run` behaviors to
// perform: normal execution, or one of the read-only introspection modes.
type runMode struct {
	list                     bool
	graph                    bool
	dryRun                   bool
	workdir                  string // "" means: resolve from the process's current directory
	localRepositoryOverrides map[string]string
	vars                     map[string]string
	debug                    bool
}

// stringSliceFlag implements flag.Value for a repeatable string flag —
// the stdlib flag package has no built-in for this. --local-repository
// can be passed multiple times, once per overridden action reference.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseLocalRepositoryOverrides parses --local-repository values into a
// map keyed by "owner/repo@ref", matching act's own flag syntax
// (--local-repository owner/repo[@ref]=local/path, repeatable) — except
// mirror-gha only supports the owner/repo@ref key form, not act's
// additional full-URL form, since mirror-gha doesn't model arbitrary git
// hosts yet.
// parseVarFlags parses --var values into a map for the vars.* expression
// context, matching act's own --var syntax exactly: "NAME=VALUE", or a
// bare "NAME" (no "=") for an empty-string value.
func parseVarFlags(values []string) map[string]string {
	vars := map[string]string{}
	for _, v := range values {
		name, value, _ := strings.Cut(v, "=")
		vars[name] = value
	}
	return vars
}

// parseVarFile reads one "NAME=VALUE" (or bare "NAME") pair per line from
// path, matching act's own --var-file format — blank lines and
// "#"-prefixed comment lines are skipped. A missing file is not an
// error (the default path, ".vars", won't exist in most repos).
func parseVarFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read var file %s: %w", path, err)
	}

	vars := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, _ := strings.Cut(line, "=")
		vars[name] = value
	}
	return vars, nil
}

// fakeRuntimeToken builds a JWT-shaped (but unsigned and unverified)
// ACTIONS_RUNTIME_TOKEN value. Found necessary via real end-to-end
// testing: @actions/upload-artifact@v4's bundled client runs the real
// token through a jwt-decode library before making any request — a
// plain placeholder string (no "." separators) makes
// `token.split(".")[1]` return undefined, crashing with "Invalid token
// specified: Cannot read properties of undefined (reading 'replace')"
// before a single HTTP request is even made. Neither mirror-gha's cache
// server nor its artifact server ever validates this token, so any
// JWT-shaped value works — only the shape (three dot-separated
// base64url segments, with the payload segment decoding to valid JSON)
// needs to be real.
func fakeRuntimeToken() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"scp":"Actions.Results:1:1","exp":9999999999}`))
	return header + "." + payload + ".mirror-gha-local-signature"
}

// fakeGitHubToken is a plain, clearly-fake GITHUB_TOKEN placeholder — a
// real GITHUB_TOKEN is an opaque string (not JWT-shaped, unlike
// ACTIONS_RUNTIME_TOKEN above), so no particular shape is needed here.
// Its purpose is just to exist: many real actions (actions/checkout,
// actions/github-script, etc.) read process.env.GITHUB_TOKEN directly and
// behave differently — sometimes erroring — if it's empty or missing.
// mirror-gha performs no real GitHub API calls with it (permissions:
// parsing is shim-only, matching this project's documented v1 scope),
// so the value itself is never validated against anything.
func fakeGitHubToken() string {
	return "ghs_mirror_gha_local_placeholder_token"
}

func parseLocalRepositoryOverrides(values []string) (map[string]string, error) {
	overrides := map[string]string{}
	for _, v := range values {
		key, path, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("--local-repository %q must be in the form owner/repo[@ref]=local/path", v)
		}
		overrides[key] = path
	}
	return overrides, nil
}

// runMain parses `run` subcommand flags and dispatches to runCommand.
// Split out from main() so flag parsing itself doesn't need a live
// process to test.
func runMain(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	list := fs.Bool("list", false, "list jobs (and matrix combinations) without running them")
	graph := fs.Bool("graph", false, "print the job dependency graph without running")
	dryRun := fs.Bool("dryrun", false, "run the full needs/matrix/if orchestration without executing any step")
	workdir := fs.String("workdir", "", "directory to bind-mount as the job workspace (default: current directory)")
	var localRepos stringSliceFlag
	fs.Var(&localRepos, "local-repository", "override local action resolution: owner/repo[@ref]=local/path (repeatable)")
	var varFlags stringSliceFlag
	fs.Var(&varFlags, "var", "variable to make available to the vars.* context: NAME=VALUE or bare NAME (repeatable)")
	varFile := fs.String("var-file", ".vars", "file with NAME=VALUE vars.* entries, one per line (missing file is not an error)")
	debug := fs.Bool("debug", false, "show ::debug:: workflow command output and export ACTIONS_STEP_DEBUG=true to steps (hidden by default, matching real GitHub Actions)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mirror run [--list|--graph|--dryrun] [--workdir <path>] [--local-repository owner/repo[@ref]=local/path] [--var NAME=VALUE] [--var-file <path>] <workflow.yml>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1 // flag package already printed the error and usage
	}

	overrides, err := parseLocalRepositoryOverrides(localRepos)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	vars, err := parseVarFile(*varFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for k, v := range parseVarFlags(varFlags) {
		vars[k] = v
	}

	rest := fs.Args()
	if len(rest) < 1 {
		fs.Usage()
		return 1
	}

	return runCommand(rest[0], runMode{list: *list, graph: *graph, dryRun: *dryRun, workdir: *workdir, localRepositoryOverrides: overrides, vars: vars, debug: *debug})
}

// runCommand parses the workflow at path and, depending on mode, either
// lists jobs, prints the dependency graph, or runs every job in
// dependency order (dry or for real). It returns the process exit code:
// 0 on success, 1 otherwise — split out from main() so it's directly
// testable without spawning a subprocess.
func runCommand(path string, mode runMode) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read workflow: %v\n", err)
		return 1
	}

	wf, err := engine.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse workflow: %v\n", err)
		return 1
	}

	switch {
	case mode.list:
		return listJobs(wf)
	case mode.graph:
		return printGraph(wf)
	}

	workspaceDir := mode.workdir
	if workspaceDir == "" {
		workspaceDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve current directory: %v\n", err)
			return 1
		}
	}
	workspaceDir, err = filepath.Abs(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve workspace directory: %v\n", err)
		return 1
	}

	selectBackend := runner.SelectBackend
	if mode.dryRun {
		selectBackend = func(runsOn string) (runner.Backend, error) {
			// Still validate runner support in dry-run mode — "would this
			// even run here" is exactly the question dry-run answers, so
			// an unsupported runs-on should still error, not fake success.
			if _, err := runner.SelectBackend(runsOn); err != nil {
				return nil, err
			}
			return runner.DryRunBackend{}, nil
		}
	}

	extraEnv := map[string]string{
		"GITHUB_TOKEN": fakeGitHubToken(),
	}
	if mode.debug {
		extraEnv["ACTIONS_STEP_DEBUG"] = "true"
	}
	if !mode.dryRun {
		storeRoot, err := cacheserver.StoreRoot()
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve cache store root: %v\n", err)
			return 1
		}
		store, err := cacheserver.OpenStore(storeRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open cache store: %v\n", err)
			return 1
		}
		cacheSrv, err := cacheserver.Start(store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start cache server: %v\n", err)
			return 1
		}
		defer cacheSrv.Stop(context.Background())

		extraEnv["ACTIONS_CACHE_URL"] = fmt.Sprintf("http://host.docker.internal:%d/", cacheSrv.Port())
		extraEnv["ACTIONS_RUNTIME_TOKEN"] = fakeRuntimeToken()

		artifactStore, artifactRoot, err := artifactserver.NewTempStore()
		if err != nil {
			fmt.Fprintf(os.Stderr, "create artifact store: %v\n", err)
			return 1
		}
		artifactSrv, err := artifactserver.Start(artifactStore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start artifact server: %v\n", err)
			return 1
		}
		defer artifactSrv.Stop(context.Background())
		defer fmt.Printf("artifacts stored at: %s\n", artifactRoot)

		extraEnv["ACTIONS_RUNTIME_URL"] = fmt.Sprintf("http://host.docker.internal:%d/", artifactSrv.Port())
		extraEnv["ACTIONS_RESULTS_URL"] = extraEnv["ACTIONS_RUNTIME_URL"]
	}

	result, err := engine.RunWorkflow(context.Background(), wf, selectBackend, workspaceDir, mode.localRepositoryOverrides, mode.vars, extraEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	overallExit := 0
	for _, name := range result.Order {
		jr := result.Jobs[name]
		for _, s := range jr.Steps {
			fmt.Printf("[%s] %s: %s\n", name, s.Name, s.Conclusion)
			if s.Stdout != "" {
				fmt.Print(s.Stdout)
			}
			if s.Stderr != "" {
				fmt.Fprint(os.Stderr, s.Stderr)
			}
		}
		if jr.Conclusion == "failure" {
			overallExit = 1
		}
	}

	return overallExit
}

// listJobs prints every job (and, for matrix jobs, every combination)
// with its `runs-on` and `needs:`, without running anything.
func listJobs(wf *engine.Workflow) int {
	names := make([]string, 0, len(wf.Jobs))
	for n := range wf.Jobs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		job := wf.Jobs[name]
		combos, err := engine.ExpandMatrix(job.Strategy)
		if err != nil {
			fmt.Fprintf(os.Stderr, "job %s: %v\n", name, err)
			return 1
		}
		for _, combo := range combos {
			line := name + engine.MatrixSuffix(combo) + "\truns-on=" + job.RunsOn
			if len(job.Needs) > 0 {
				line += "\tneeds=" + strings.Join(job.Needs, ",")
			}
			fmt.Println(line)
		}
	}
	return 0
}

// printGraph prints jobs in dependency (topological) order, with each
// job's `needs:` indented underneath it.
func printGraph(wf *engine.Workflow) int {
	order, err := engine.TopoSortJobs(wf.Jobs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	for _, name := range order {
		fmt.Println(name)
		for _, dep := range wf.Jobs[name].Needs {
			fmt.Printf("  needs: %s\n", dep)
		}
	}
	return 0
}

func printUsage() {
	fmt.Println("usage: mirror <version|run|dashboard>")
}
