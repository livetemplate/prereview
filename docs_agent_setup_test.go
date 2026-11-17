package main

import (
	"os"
	"strings"
	"testing"
)

// TestAgentSetupDoc keeps docs/agent-setup.md (#113), the page an agent is
// pointed at to install prereview, in step with clientTargets(): every agent
// has its --client id and installed path in the table, and no command runs
// --install-skill without --client (with no terminal, the menu silently
// installs the Claude skill). The file's URL is public, so it must not move.
func TestAgentSetupDoc(t *testing.T) {
	b, err := os.ReadFile("docs/agent-setup.md")
	if err != nil {
		t.Fatalf("docs/agent-setup.md is linked from outside the repo and must stay at that path: %v", err)
	}
	doc := string(b)
	for _, target := range clientTargets() {
		if want := "--client=" + target.id; !strings.Contains(doc, want) {
			t.Errorf("no %q in docs/agent-setup.md", want)
		}
		if want := "~/" + target.files[0].rel; !strings.Contains(doc, want) {
			t.Errorf("%s's installed file %q isn't in docs/agent-setup.md", target.id, want)
		}
	}
	for i, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, "--install-skill") && !strings.Contains(line, "--client=") {
			t.Errorf("docs/agent-setup.md:%d runs --install-skill without --client: %s", i+1, line)
		}
	}
}
