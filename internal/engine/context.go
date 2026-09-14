package engine

import (
	"fmt"
	"strings"
)

// StepOutcome records what happened when a step ran, for later steps'
// `if:` conditions and ${{ steps.<id>.* }} references.
type StepOutcome struct {
	Outcome string // "success", "failure", or "skipped"
	Outputs map[string]string
}

// Context is the set of GitHub Actions contexts (github, env, runner, steps)
// available to expressions while a job runs.
type Context struct {
	GitHub map[string]interface{}
	Env    map[string]string
	Runner map[string]interface{}
	Steps  map[string]StepOutcome
}

// NewContext builds the initial Context for running job within workflow.
// Env is the merge of workflow-level and job-level env (job wins on conflict).
func NewContext(wf *Workflow, job *Job) *Context {
	env := map[string]string{}
	for k, v := range wf.Env {
		env[k] = v
	}
	for k, v := range job.Env {
		env[k] = v
	}

	return &Context{
		GitHub: map[string]interface{}{
			"event_name": "workflow_dispatch",
			"ref":        "refs/heads/main",
			"sha":        "0000000000000000000000000000000000000000",
			"repository": "local/mirror-gha",
			"workflow":   wf.Name,
			"actor":      "local",
		},
		Env: env,
		Runner: map[string]interface{}{
			"os":   "Linux",
			"temp": "/tmp",
		},
		Steps: map[string]StepOutcome{},
	}
}

// lookupStringCI does a case-insensitive lookup against a string-valued map.
// GitHub Actions' expression language matches context/property names
// case-insensitively (dot access is lowercased at parse time — see
// third_party/ghaexpr/parser.go — but bracket-string access like
// env['SKIP'] preserves case), so lookups here must not assume either.
func lookupStringCI(m map[string]string, key string) string {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func lookupAnyCI(m map[string]interface{}, key string) interface{} {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}

func (c *Context) resolvePath(path []string) (interface{}, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("empty context path")
	}
	switch strings.ToLower(path[0]) {
	case "env":
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid env reference: %s", strings.Join(path, "."))
		}
		return lookupStringCI(c.Env, path[1]), nil
	case "github":
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid github reference: %s", strings.Join(path, "."))
		}
		return lookupAnyCI(c.GitHub, path[1]), nil
	case "runner":
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid runner reference: %s", strings.Join(path, "."))
		}
		return lookupAnyCI(c.Runner, path[1]), nil
	case "steps":
		if len(path) < 3 {
			return nil, fmt.Errorf("invalid steps reference: %s", strings.Join(path, "."))
		}
		var outcome StepOutcome
		var found bool
		for id, o := range c.Steps {
			if strings.EqualFold(id, path[1]) {
				outcome, found = o, true
				break
			}
		}
		if !found {
			return nil, nil
		}
		switch strings.ToLower(path[2]) {
		case "outcome":
			return outcome.Outcome, nil
		case "outputs":
			if len(path) != 4 {
				return nil, fmt.Errorf("invalid steps.outputs reference: %s", strings.Join(path, "."))
			}
			return lookupStringCI(outcome.Outputs, path[3]), nil
		default:
			return nil, fmt.Errorf("unknown steps field: %s", path[2])
		}
	default:
		return nil, fmt.Errorf("unknown context: %s", path[0])
	}
}

func (c *Context) callStatusFunc(name string) (interface{}, error) {
	anyFailed := false
	for _, s := range c.Steps {
		if s.Outcome == "failure" {
			anyFailed = true
		}
	}
	switch name {
	case "success":
		return !anyFailed, nil
	case "failure":
		return anyFailed, nil
	case "always":
		return true, nil
	case "cancelled":
		return false, nil
	default:
		return nil, fmt.Errorf("unknown function %s()", name)
	}
}
