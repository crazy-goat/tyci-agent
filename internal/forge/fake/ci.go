package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

// Comment is one comment that CI.Comment recorded.
type Comment struct {
	Issue int
	Body  string
}

// CI is an in-memory forge.CIReader and forge.Commenter. It is safe for
// concurrent use. Set the fields with the setters while a test runs.
type CI struct {
	mu       sync.Mutex
	runs     map[string]forge.CIRun // key: workflow + " " + branch
	err      error
	comments []Comment
}

var (
	_ forge.CIReader  = (*CI)(nil)
	_ forge.Commenter = (*CI)(nil)
)

// SetRun sets the newest completed run of workflow on branch.
func (c *CI) SetRun(workflow, branch string, r forge.CIRun) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs == nil {
		c.runs = map[string]forge.CIRun{}
	}
	c.runs[workflow+" "+branch] = r
}

// SetErr makes LatestCI return err (nil clears it).
func (c *CI) SetErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// LatestCI returns the run that SetRun set.
func (c *CI) LatestCI(_ context.Context, workflow, branch string) (forge.CIRun, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return forge.CIRun{}, false, c.err
	}
	r, ok := c.runs[workflow+" "+branch]
	return r, ok, nil
}

// Comment records the comment.
func (c *CI) Comment(_ context.Context, issue int, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if issue <= 0 {
		return fmt.Errorf("fake: invalid issue number %d", issue)
	}
	c.comments = append(c.comments, Comment{Issue: issue, Body: body})
	return nil
}

// Comments returns a copy of the recorded comments.
func (c *CI) Comments() []Comment {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Comment(nil), c.comments...)
}
