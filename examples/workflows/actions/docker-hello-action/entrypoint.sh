#!/bin/sh
set -e

# GitHub Actions' INPUT_* convention allows dashes in input names, which
# aren't valid POSIX shell variable-name characters — $INPUT_WHO-TO-GREET
# would parse as "$INPUT_WHO" followed by a literal "-TO-GREET". printenv
# reads the raw environment entry directly, sidestepping shell variable
# syntax entirely (the same underlying fact that made mirror-gha's JS
# action support bypass the shell for exec — see runner.StepSpec's doc
# comment — just encountered from the action-author's side this time).
who="$(printenv 'INPUT_WHO-TO-GREET' || true)"
who="${who:-World}"

echo "Hello, $who! (from a Docker action)"
echo "greeting=Hello, $who!" >> "$GITHUB_OUTPUT"
