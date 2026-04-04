//go:build server

package hello

import (
	"fmt"
	"strings"

	am "github.com/adaptivemsg/adaptivemsg-go"
)

func (msg *HelloRequest) Handle(_ *am.StreamContext) (am.Message, error) {
	question := strings.ToLower(msg.Question)
	if strings.Contains(question, "error") {
		return nil, fmt.Errorf("bad request: %s", question)
	}
	answer := "I don't know"
	switch {
	case strings.Contains(question, "who are you"):
		answer = "I am hello server"
	case strings.Contains(question, "how are you"):
		answer = "I am good"
	}
	return &HelloReply{
		Answer: fmt.Sprintf("%s, %s", answer, msg.Who),
		Internal: HelloInternal{
			TraceID: fmt.Sprintf("trace-%s", msg.Who),
		},
	}, nil
}

var _ = am.MustRegisterGlobalType[HelloRequest]()
