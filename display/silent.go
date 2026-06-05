package display

import (
	"strings"

	"github.com/decodo/tyci-agent/stream"
)

type Silent struct {
	text strings.Builder
}

func NewSilent() *Silent {
	return &Silent{}
}

func (s *Silent) Thinking(text string)            {}
func (s *Silent) ToolCall(name, args, result string) {}

func (s *Silent) Text(text string) {
	s.text.WriteString(text)
}

func (s *Silent) Summary(usage stream.Usage) {}
func (s *Silent) Error(err error)             {}
func (s *Silent) End()                        {}

func (s *Silent) Text2() string {
	return s.text.String()
}
