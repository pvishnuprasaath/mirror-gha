package commands

import "testing"

func TestProcessWorkflowCommands_GroupAndEndgroup(t *testing.T) {
	display, masks := ProcessWorkflowCommands("::group::Installing deps\nnpm install\n::endgroup::\n", false)
	want := "▶ Installing deps\nnpm install\n"
	if display != want {
		t.Errorf("display = %q, want %q", display, want)
	}
	if len(masks) != 0 {
		t.Errorf("masks = %v, want none", masks)
	}
}

func TestProcessWorkflowCommands_ErrorWarningNotice(t *testing.T) {
	input := "::error::something broke\n::warning::careful\n::notice::fyi\n"
	display, _ := ProcessWorkflowCommands(input, false)
	want := "❌ something broke\n⚠️  careful\nℹ️  fyi\n"
	if display != want {
		t.Errorf("display = %q, want %q", display, want)
	}
}

func TestProcessWorkflowCommands_AnnotationWithParams(t *testing.T) {
	display, _ := ProcessWorkflowCommands("::error file=app.js,line=1,col=5::bad thing\n", false)
	want := "❌ bad thing\n"
	if display != want {
		t.Errorf("display = %q, want %q", display, want)
	}
}

func TestProcessWorkflowCommands_AddMaskExtractedAndRemoved(t *testing.T) {
	display, masks := ProcessWorkflowCommands("before\n::add-mask::supersecret\nafter\n", false)
	want := "before\nafter\n"
	if display != want {
		t.Errorf("display = %q, want %q", display, want)
	}
	if len(masks) != 1 || masks[0] != "supersecret" {
		t.Errorf("masks = %v, want [supersecret]", masks)
	}
}

func TestProcessWorkflowCommands_DebugHiddenByDefault(t *testing.T) {
	display, _ := ProcessWorkflowCommands("::debug::internal detail\nvisible line\n", false)
	want := "visible line\n"
	if display != want {
		t.Errorf("display = %q, want %q", display, want)
	}
}

func TestProcessWorkflowCommands_DebugShownWhenEnabled(t *testing.T) {
	display, _ := ProcessWorkflowCommands("::debug::internal detail\n", true)
	want := "🐛 internal detail\n"
	if display != want {
		t.Errorf("display = %q, want %q", display, want)
	}
}

func TestProcessWorkflowCommands_PlainLinesUnchanged(t *testing.T) {
	display, masks := ProcessWorkflowCommands("hello world\nsecond line\n", false)
	want := "hello world\nsecond line\n"
	if display != want {
		t.Errorf("display = %q, want %q", display, want)
	}
	if len(masks) != 0 {
		t.Errorf("masks = %v, want none", masks)
	}
}

func TestRedactMasks_ReplacesLiteralSubstring(t *testing.T) {
	got := RedactMasks("token=supersecret and again supersecret", []string{"supersecret"})
	want := "token=*** and again ***"
	if got != want {
		t.Errorf("RedactMasks() = %q, want %q", got, want)
	}
}

func TestRedactMasks_SkipsEmptyValues(t *testing.T) {
	got := RedactMasks("unchanged text", []string{""})
	if got != "unchanged text" {
		t.Errorf("RedactMasks() = %q, want unchanged", got)
	}
}
