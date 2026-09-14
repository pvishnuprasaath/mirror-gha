package main

import (
	"context"
	"fmt"
	"os"

	"mirror-gha/internal/engine"
	"mirror-gha/internal/runner"
)

const version = "0.0.1-dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("mirror", version)
	case "run":
		if len(os.Args) < 3 {
			fmt.Println("usage: mirror run <workflow.yml>")
			os.Exit(1)
		}
		os.Exit(runCommand(os.Args[2]))
	case "dashboard":
		fmt.Println("mirror dashboard: dashboard server not implemented yet")
	default:
		printUsage()
		os.Exit(1)
	}
}

// runCommand parses and executes every job in the workflow at path,
// printing per-step results. It returns the process exit code: 0 if every
// job succeeded, 1 otherwise — split out from main() so it's directly
// testable without spawning a subprocess.
func runCommand(path string) int {
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

	for name, job := range wf.Jobs {
		backend, err := runner.SelectBackend(job.RunsOn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "job %s: %v\n", name, err)
			return 1
		}

		job := job
		result, err := engine.RunJob(context.Background(), wf, &job, backend)
		if err != nil {
			fmt.Fprintf(os.Stderr, "job %s: %v\n", name, err)
			return 1
		}

		for _, s := range result.Steps {
			fmt.Printf("[%s] %s: %s\n", name, s.Name, s.Conclusion)
			if s.Stdout != "" {
				fmt.Print(s.Stdout)
			}
			if s.Stderr != "" {
				fmt.Fprint(os.Stderr, s.Stderr)
			}
		}

		if result.Conclusion != "success" {
			return 1
		}
	}

	return 0
}

func printUsage() {
	fmt.Println("usage: mirror <version|run|dashboard>")
}
