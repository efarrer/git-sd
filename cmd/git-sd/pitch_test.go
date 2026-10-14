package main

import (
	"os"
	"strings"
	"testing"
)

// The benefit (several unrelated streams of work on one branch, each its own
// stack of PRs) is stated everywhere a newcomer looks, and the agent reference
// tells an agent not to branch.
func TestTheStreamsBenefitIsStatedEverywhere(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../../SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"README.md":      {"unrelated", "stack of PRs"},
		"SPEC.md":        {"unrelated", "stack of PRs"},
		"git sd (usage)": {"streams of work", "stack of PRs"},
		"git sd help":    {"unrelated", "stack of PRs"},
		"git sd --agent": {"unrelated", "stack of GitHub Pull Requests"},
	}
	texts := map[string]string{
		"README.md": string(readme), "SPEC.md": string(spec),
		"git sd (usage)": usageText(), "git sd help": manualText(), "git sd --agent": agentUsageText(),
	}
	for name, phrases := range want {
		for _, p := range phrases {
			if !strings.Contains(texts[name], p) {
				t.Errorf("%s does not mention %q", name, p)
			}
		}
	}
	agent := agentUsageText()
	for _, g := range []string{"WORKFLOW MODEL", "Do NOT create, switch or rebase", "'git sd assign <set>:<index>'", "same Stack Set"} {
		if !strings.Contains(agent, g) {
			t.Errorf("--agent lacks the guidance %q", g)
		}
	}
	if !strings.HasPrefix(usageText(), "usage: git sd <command> [<args>]\n") {
		t.Errorf("the usage still starts with its usage line")
	}
	if strings.Contains(string(readme), "designed to enable a \"stacked diff\" workflow") {
		t.Errorf("the old one-sentence pitch is gone")
	}
}
