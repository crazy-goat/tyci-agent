package main

import (
	"strings"

	"github.com/crazy-goat/tyci-agent/stream"
)

// collector captures agent output (simplified for subagent runner)
type collector struct {
	text strings.Builder
}

func (c *collector) Thinking(text string)                           { c.text.WriteString(text) }
func (c *collector) Text(text string)                               { c.text.WriteString(text) }
func (c *collector) Request(string)                                 {}
func (c *collector) ToolCallStart(name string)                      {}
func (c *collector) ToolCallDelta(delta string)                     {}
func (c *collector) ToolCallEnd(name, result string)                {}
func (c *collector) ToolFinish()                                    {}
func (c *collector) ToolBlock(msg string)                           {}
func (c *collector) Summary(usage stream.Usage, stats stream.Stats) {}
func (c *collector) Total(usage stream.Usage)                       {}
func (c *collector) Error(err error)                                {}
func (c *collector) End()                                           {}
