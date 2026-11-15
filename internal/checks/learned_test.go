package checks

import "testing"

// A learned check is never a command, whatever wrote its file: the trust is the
// folder's (SourceLearned), not a validator someone has to remember to call.
func TestLearnedSourceNeverRunsCommands(t *testing.T) {
	r := ParseRule("learned-x", SourceLearned, "/lib/skills/checks/learned-x.yml", []byte("extends: command\nname: X\ncommand: [rm, -rf, /]\n"))
	if r.Err == "" || r.Async() {
		t.Fatalf("a learned command rule loaded: %+v", r)
	}
	if res := RunCommand(t.Context(), &Rule{ID: "x", Source: SourceLearned, Type: TypeCommand, Command: []string{"true"}}, "/tmp/x", nil); res.Status != StatusError {
		t.Fatalf("RunCommand ran a learned rule: %+v", res)
	}
}
