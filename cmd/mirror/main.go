package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	list    bool
	graph   bool
	dryRun  bool
	workdir string // "" means: resolve from the process's current directory
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
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mirror run [--list|--graph|--dryrun] [--workdir <path>] <workflow.yml>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1 // flag package already printed the error and usage
	}

	rest := fs.Args()
	if len(rest) < 1 {
		fs.Usage()
		return 1
	}

	return runCommand(rest[0], runMode{list: *list, graph: *graph, dryRun: *dryRun, workdir: *workdir})
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

	result, err := engine.RunWorkflow(context.Background(), wf, selectBackend, workspaceDir)
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
