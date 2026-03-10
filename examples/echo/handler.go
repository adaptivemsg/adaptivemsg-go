//go:build server

package echo

import (
	"errors"
	"time"

	am "adaptivemsg"
)

type StatContext interface {
	IncCounter()
	Subscribe() chan string
	Unsubscribe(chan string)
	ListClients() string
}

func (msg *MessageRequest) Handle(ctx *am.StreamContext) (am.Message, error) {
	mgr, err := statFromContext(ctx)
	if err != nil {
		return nil, err
	}
	mgr.IncCounter()
	msg.Msg += "!"
	msg.Num++
	time.Sleep(500 * time.Millisecond)
	return &MessageReply{
		Msg:       msg.Msg,
		Num:       msg.Num,
		Signature: "yours echo.v1.0",
	}, nil
}

var _ = am.MustRegisterGlobalMethod[MessageRequest]()

func (msg *SubWhoElseEvent) Handle(ctx *am.StreamContext) (am.Message, error) {
	mgr, err := statFromContext(ctx)
	if err != nil {
		return nil, err
	}
	rx := mgr.Subscribe()
	if err := ctx.NewTask(func(stream *am.Stream[am.Message]) {
		defer mgr.Unsubscribe(rx)
		for addr := range rx {
			if err := stream.Send(&WhoElseEvent{Addr: addr}); err != nil {
				return
			}
		}
	}); err != nil {
		mgr.Unsubscribe(rx)
		return nil, err
	}
	return nil, nil
}

var _ = am.MustRegisterGlobalMethod[SubWhoElseEvent]()

func (msg *WhoElse) Handle(ctx *am.StreamContext) (am.Message, error) {
	mgr, err := statFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return &WhoElseReply{Clients: mgr.ListClients()}, nil
}

var _ = am.MustRegisterGlobalMethod[WhoElse]()

func (msg *MessageTimeout) Handle(_ *am.StreamContext) (am.Message, error) {
	time.Sleep(time.Duration(msg.Secs) * time.Second)
	return nil, nil
}

var _ = am.MustRegisterGlobalMethod[MessageTimeout]()

func statFromContext(ctx *am.StreamContext) (StatContext, error) {
	mgr, ok := am.ContextAs[StatContext](ctx.Context())
	if !ok || mgr == nil {
		return nil, errors.New("missing stream context")
	}
	return mgr, nil
}
