package main

import (
	"os"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/tools"
)

// workflowManager runs the chat workflow tools' runs (workflow_start and
// friends) of the repository of the current directory. Notices go to the bus,
// read at call time so tests can swap appBus.
var workflowManager = flow.NewManager(workflowNotify, tools.RunSubagentTask)

// workflowRepos routes the chat workflow tools to workflowManager and to one
// Manager per repository of ~/.tyci/config.json with a path (#218).
var workflowRepos = &flow.Repos{Primary: workflowManager, Registry: registryRepos, New: newRepoManager}

func workflowNotify(text string) { publishNotice(appBus, "", text, false) }

func registryRepos() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return flow.RegistryRepos(home)
}

// newRepoManager builds the Manager of a registry repository. Its notices
// start with the repository name.
func newRepoManager(repo string) (*flow.Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	info, err := flow.OpenRepo(home, repo)
	if err != nil {
		return nil, err
	}
	m := flow.NewManager(func(text string) { workflowNotify(repo + ": " + text) }, tools.RunSubagentTask)
	m.Info = func() (flow.RepoInfo, error) { return info, nil }
	return m, nil
}
