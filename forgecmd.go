package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge/gitlab"
	"github.com/crazy-goat/tyci-agent/internal/redact"
)

// forgeOp is one operation of `tyci forge`. args are the operands after the
// op name.
type forgeOp struct {
	usage string
	n     int
	run   func(ctx context.Context, g *gitlab.GitLab, args []string, out io.Writer) (any, error)
}

func iidArg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return n, nil
}

// withIID runs f with the first operand as a merge request or issue number.
func withIID(f func(ctx context.Context, g *gitlab.GitLab, iid int, args []string) (any, error)) func(context.Context, *gitlab.GitLab, []string, io.Writer) (any, error) {
	return func(ctx context.Context, g *gitlab.GitLab, args []string, _ io.Writer) (any, error) {
		iid, err := iidArg(args[0])
		if err != nil {
			return nil, err
		}
		return f(ctx, g, iid, args[1:])
	}
}

func readBody(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// forgeOps are the operations of the GitLab path of the check scripts and
// tasks. A JSON result has the shape of the matching gh --json output.
var forgeOps = map[string]forgeOp{
	"issue-view": {"N", 1, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, _ []string) (any, error) {
		return g.Issue(ctx, n)
	})},
	"issue-assign-self": {"N", 1, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, _ []string) (any, error) {
		return nil, g.AssignSelf(ctx, n)
	})},
	"issue-search": {"WORDS", 1, func(ctx context.Context, g *gitlab.GitLab, a []string, _ io.Writer) (any, error) {
		return g.SearchIssues(ctx, a[0])
	}},
	"issue-comment": {"N BODY-FILE", 2, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, a []string) (any, error) {
		body, err := readBody(a[0])
		if err != nil {
			return nil, err
		}
		return nil, g.CommentIssue(ctx, n, body)
	})},
	"issue-create": {"TITLE BODY-FILE", 2, func(ctx context.Context, g *gitlab.GitLab, a []string, _ io.Writer) (any, error) {
		body, err := readBody(a[1])
		if err != nil {
			return nil, err
		}
		return g.CreateIssue(ctx, a[0], body)
	}},
	"can-write": {"USER", 1, func(ctx context.Context, g *gitlab.GitLab, a []string, _ io.Writer) (any, error) {
		ok, err := g.CanWrite(ctx, a[0])
		if ok {
			return "write", err
		}
		return "none", err
	}},
	"whoami": {"", 0, func(ctx context.Context, g *gitlab.GitLab, _ []string, _ io.Writer) (any, error) {
		return g.Whoami(ctx)
	}},
	"pr-list": {"BRANCH", 1, func(ctx context.Context, g *gitlab.GitLab, a []string, _ io.Writer) (any, error) {
		return g.PRs(ctx, a[0])
	}},
	"pr-create": {"BASE HEAD TITLE BODY", 4, func(ctx context.Context, g *gitlab.GitLab, a []string, _ io.Writer) (any, error) {
		return g.CreatePR(ctx, a[0], a[1], a[2], a[3])
	}},
	"pr-comment": {"N BODY-FILE", 2, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, a []string) (any, error) {
		body, err := readBody(a[0])
		if err != nil {
			return nil, err
		}
		return nil, g.CommentPR(ctx, n, body)
	})},
	"pr-comments": {"N", 1, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, _ []string) (any, error) {
		return g.PRComments(ctx, n)
	})},
	"pr-view": {"N", 1, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, _ []string) (any, error) {
		return g.PR(ctx, n)
	})},
	"pr-checks": {"N", 1, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, _ []string) (any, error) {
		return g.PRChecks(ctx, n)
	})},
	"pr-merge": {"N SHA", 2, withIID(func(ctx context.Context, g *gitlab.GitLab, n int, a []string) (any, error) {
		return nil, g.Merge(ctx, n, a[0])
	})},
	"ci-log": {"N", 1, func(ctx context.Context, g *gitlab.GitLab, a []string, out io.Writer) (any, error) {
		n, err := iidArg(a[0])
		if err != nil {
			return nil, err
		}
		return nil, g.CILog(ctx, n, out)
	}},
}

// runForge runs one `tyci forge` op for repo. The repo must be a GitLab repo
// of ~/.tyci/config.json (repos). Every error text goes through
// redact.Redact. It returns the exit code.
func runForge(ctx context.Context, home, repo string, args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "tyci forge:", redact.Redact(err.Error()))
		return 1
	}
	if len(args) == 0 {
		return fail(errors.New("missing op"))
	}
	op, ok := forgeOps[args[0]]
	if !ok {
		return fail(fmt.Errorf("unknown op %q", args[0]))
	}
	if len(args)-1 != op.n {
		return fail(fmt.Errorf("usage: tyci forge --repo GROUP/PROJECT %s %s", args[0], op.usage))
	}
	cfg, err := flowconfig.Load(home, "", false)
	if err != nil {
		return fail(err)
	}
	url := cfg.GitLabURL(repo)
	if url == "" {
		return fail(fmt.Errorf("%q is not a GitLab repo in ~/.tyci/config.json (repos)", repo))
	}
	g, err := newGitLab(repo, url)
	if err != nil {
		return fail(err)
	}
	res, err := op.run(ctx, g, args[1:], stdout)
	if err != nil {
		return fail(err)
	}
	switch v := res.(type) {
	case nil:
	case string:
		fmt.Fprintln(stdout, v)
	default:
		if err := json.NewEncoder(stdout).Encode(v); err != nil {
			return fail(err)
		}
	}
	return 0
}

var forgeRepo string

// newGitLab builds the forge of runForge. Tests replace it.
var newGitLab = gitlab.New

// forgeCmd is the GitLab helper of the check scripts. It is hidden: the
// scripts call it through $TYCI_BIN, people do not.
var forgeCmd = &cobra.Command{
	Use:    "forge --repo GROUP/PROJECT OP [ARGS...]",
	Short:  "GitLab operations for the workflow check scripts",
	Hidden: true,
	Args:   cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "tyci forge:", err)
			os.Exit(1)
		}
		if code := runForge(cmd.Context(), home, forgeRepo, args, cmd.OutOrStdout(), cmd.ErrOrStderr()); code != 0 {
			os.Exit(code)
		}
	},
}

func init() {
	forgeCmd.Flags().StringVar(&forgeRepo, "repo", "", "GitLab project (group/project)")
	// Flags end at the op name: a title such as "-x" stays an operand.
	forgeCmd.Flags().SetInterspersed(false)
	rootCmd.AddCommand(forgeCmd)
}
