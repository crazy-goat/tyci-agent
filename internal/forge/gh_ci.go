package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

var (
	_ CIReader  = (*GitHub)(nil)
	_ Commenter = (*GitHub)(nil)
)

// LatestCI implements CIReader with 'gh run list'. gh lists the newest run first.
func (g *GitHub) LatestCI(ctx context.Context, workflow, branch string) (CIRun, bool, error) {
	out, err := g.run(ctx, g.bin, "run", "list", "-R", g.repo, "--workflow", workflow, "--branch", branch,
		"--limit", "20", "--json", "databaseId,conclusion,status,headSha,createdAt")
	if err != nil {
		return CIRun{}, false, err
	}
	var runs []struct {
		ID         int64     `json:"databaseId"`
		Conclusion string    `json:"conclusion"`
		Status     string    `json:"status"`
		SHA        string    `json:"headSha"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return CIRun{}, false, fmt.Errorf("forge: bad JSON: %w", err)
	}
	for _, r := range runs {
		if r.Status != "completed" {
			continue
		}
		switch r.Conclusion {
		case "cancelled", "skipped", "neutral":
			continue
		}
		return CIRun{ID: r.ID, SHA: r.SHA, Failed: r.Conclusion != "success", CreatedAt: r.CreatedAt}, true, nil
	}
	return CIRun{}, false, nil
}

// Comment implements Commenter with 'gh api'.
func (g *GitHub) Comment(ctx context.Context, issue int, body string) error {
	if issue <= 0 {
		return fmt.Errorf("forge: invalid issue number %d", issue)
	}
	_, err := g.run(ctx, g.bin, "api", "-X", "POST", "repos/"+g.repo+"/issues/"+strconv.Itoa(issue)+"/comments", "-f", "body="+body)
	return err
}
