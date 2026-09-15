package engine

// DefaultEventPayload returns a synthetic, structurally-real GitHub
// webhook payload for the six trigger types covered here (matching
// GitHub's own public webhook-payload documentation — act has no
// reference implementation for this at all, confirmed via source: it
// never fabricates a per-event payload, only ever loading a real
// user-supplied one or falling back to "{}"). Populated with the same
// placeholder conventions this project already uses elsewhere (a
// 40-zero sha, "local/mirror-gha" as the repository, "refs/heads/main").
// Any trigger name outside these six falls back to "{}", matching act's
// own default for the untyped case rather than attempting exhaustive
// coverage of GitHub's several dozen trigger types.
func DefaultEventPayload(eventName string) string {
	const zeroSHA = "0000000000000000000000000000000000000000"
	switch eventName {
	case "push":
		return `{
  "ref": "refs/heads/main",
  "before": "` + zeroSHA + `",
  "after": "` + zeroSHA + `",
  "repository": {"full_name": "local/mirror-gha", "default_branch": "main"},
  "pusher": {"name": "local", "email": "local@example.com"},
  "head_commit": {"id": "` + zeroSHA + `", "message": "local test run", "author": {"name": "local"}}
}`
	case "pull_request":
		return `{
  "action": "opened",
  "number": 1,
  "pull_request": {
    "number": 1,
    "title": "Local test pull request",
    "state": "open",
    "draft": false,
    "merged": false,
    "head": {"ref": "feature-branch", "sha": "` + zeroSHA + `"},
    "base": {"ref": "main", "sha": "` + zeroSHA + `"},
    "user": {"login": "local"}
  },
  "repository": {"full_name": "local/mirror-gha"}
}`
	case "workflow_dispatch":
		return `{"inputs": {}, "ref": "refs/heads/main", "repository": {"full_name": "local/mirror-gha"}}`
	case "workflow_call":
		return `{"inputs": {}}`
	case "repository_dispatch":
		return `{"action": "mirror-local-event", "client_payload": {}, "repository": {"full_name": "local/mirror-gha"}}`
	case "workflow_run":
		return `{
  "action": "completed",
  "workflow_run": {
    "id": 1,
    "name": "local test run",
    "head_branch": "main",
    "head_sha": "` + zeroSHA + `",
    "status": "completed",
    "conclusion": "success",
    "event": "push"
  }
}`
	default:
		return "{}"
	}
}
