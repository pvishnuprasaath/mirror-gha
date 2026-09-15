package engine

import (
	"encoding/json"
	"testing"
)

func TestDefaultEventPayload_UnknownEventIsEmptyObject(t *testing.T) {
	got := DefaultEventPayload("release")
	if got != "{}" {
		t.Errorf("DefaultEventPayload(release) = %q, want {}", got)
	}
}

func TestDefaultEventPayload_Push(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("push")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["ref"] != "refs/heads/main" {
		t.Errorf("ref = %v, want refs/heads/main", payload["ref"])
	}
	headCommit, ok := payload["head_commit"].(map[string]interface{})
	if !ok || headCommit["message"] == "" {
		t.Errorf("head_commit.message missing or empty: %v", payload["head_commit"])
	}
	pusher, ok := payload["pusher"].(map[string]interface{})
	if !ok || pusher["name"] != "local" {
		t.Errorf("pusher.name = %v, want local", payload["pusher"])
	}
}

func TestDefaultEventPayload_PullRequest(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("pull_request")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["action"] != "opened" {
		t.Errorf("action = %v, want opened", payload["action"])
	}
	pr, ok := payload["pull_request"].(map[string]interface{})
	if !ok {
		t.Fatalf("pull_request missing: %v", payload)
	}
	head, ok := pr["head"].(map[string]interface{})
	if !ok || head["ref"] == "" {
		t.Errorf("pull_request.head.ref missing: %v", pr)
	}
	base, ok := pr["base"].(map[string]interface{})
	if !ok || base["ref"] != "main" {
		t.Errorf("pull_request.base.ref = %v, want main", base)
	}
}

func TestDefaultEventPayload_WorkflowDispatch(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("workflow_dispatch")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := payload["inputs"].(map[string]interface{}); !ok {
		t.Errorf("inputs missing or wrong type: %v", payload["inputs"])
	}
}

func TestDefaultEventPayload_WorkflowCall(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("workflow_call")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := payload["inputs"].(map[string]interface{}); !ok {
		t.Errorf("inputs missing or wrong type: %v", payload["inputs"])
	}
}

func TestDefaultEventPayload_RepositoryDispatch(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("repository_dispatch")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["action"] == "" {
		t.Errorf("action missing: %v", payload)
	}
	if _, ok := payload["client_payload"].(map[string]interface{}); !ok {
		t.Errorf("client_payload missing or wrong type: %v", payload["client_payload"])
	}
}

func TestDefaultEventPayload_WorkflowRun(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("workflow_run")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	wr, ok := payload["workflow_run"].(map[string]interface{})
	if !ok {
		t.Fatalf("workflow_run missing: %v", payload)
	}
	if wr["conclusion"] != "success" {
		t.Errorf("workflow_run.conclusion = %v, want success", wr["conclusion"])
	}
}
