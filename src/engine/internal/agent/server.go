package agent

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"
)

// Runner runs the daemon's model once: the system prompt, the input and the action schema in, the
// model's raw reply and what the run used out.
type Runner interface {
	Run(ctx context.Context, system, input string, schema []byte) (raw []byte, usage *agentv1.Usage, err error)
}

// Login is the daemon's CLI sign-in.
type Login interface {
	Status(ctx context.Context) (*agentv1.StatusResponse, error)
	Start(ctx context.Context) (*agentv1.LoginStartResponse, error)
	Finish(ctx context.Context, code string) error
	Logout(ctx context.Context) error
}

// NewServer serves the agent contract with r and l; model is the model r runs, which Status says.
func NewServer(r Runner, l Login, model string) agentv1connect.AgentServiceHandler {
	return &server{r: r, l: l, model: model}
}

type server struct {
	r     Runner
	l     Login
	model string
}

// Step runs the model; a reply that does not parse is retried once with the error told. Nothing
// of the conversation is logged: it carries the owner's words.
func (s *server) Step(ctx context.Context, req *connect.Request[agentv1.StepRequest]) (*connect.Response[agentv1.StepResponse], error) {
	schema := ActionSchema(req.Msg.Tools)
	input := Render(req.Msg)
	var perr error
	var total *agentv1.Usage
	for try := 0; try < 2; try++ {
		in := input
		if perr != nil {
			in = fmt.Sprintf("%s\nYour reply was not valid: %v. Reply with exactly one action.\n", input, perr)
		}
		raw, usage, err := s.r.Run(ctx, Rules, in, schema)
		if err != nil {
			return nil, coded(err)
		}
		total = addUsage(total, usage)
		resp, err := Parse(raw, req.Msg.Tools)
		if err == nil {
			resp.Usage = total
			return connect.NewResponse(resp), nil
		}
		perr = err
	}
	return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("the reply was not a valid action: %w", perr))
}

func (s *server) Status(ctx context.Context, _ *connect.Request[agentv1.StatusRequest]) (*connect.Response[agentv1.StatusResponse], error) {
	r, err := s.l.Status(ctx)
	if err != nil {
		return nil, coded(err)
	}
	r.Model = s.model
	return connect.NewResponse(r), nil
}

func (s *server) LoginStart(ctx context.Context, _ *connect.Request[agentv1.LoginStartRequest]) (*connect.Response[agentv1.LoginStartResponse], error) {
	r, err := s.l.Start(ctx)
	if err != nil {
		return nil, coded(err)
	}
	return connect.NewResponse(r), nil
}

func (s *server) LoginFinish(ctx context.Context, req *connect.Request[agentv1.LoginFinishRequest]) (*connect.Response[agentv1.LoginFinishResponse], error) {
	if err := s.l.Finish(ctx, req.Msg.Code); err != nil {
		return nil, coded(err)
	}
	return connect.NewResponse(&agentv1.LoginFinishResponse{}), nil
}

func (s *server) Logout(ctx context.Context, _ *connect.Request[agentv1.LogoutRequest]) (*connect.Response[agentv1.LogoutResponse], error) {
	if err := s.l.Logout(ctx); err != nil {
		return nil, coded(err)
	}
	return connect.NewResponse(&agentv1.LogoutResponse{}), nil
}

// coded keeps the code of an error that already has one (a daemon's "not signed in" must reach
// the engine as it is); any other error is internal.
func coded(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeInternal, err)
}

// addUsage sums two attempts' usage; the cost is known only if both were, the model is the last's.
func addUsage(a, b *agentv1.Usage) *agentv1.Usage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &agentv1.Usage{
		Model:        b.Model,
		InputTokens:  a.InputTokens + b.InputTokens,
		OutputTokens: a.OutputTokens + b.OutputTokens,
		CostUsd:      a.CostUsd + b.CostUsd,
		CostKnown:    a.CostKnown && b.CostKnown,
	}
}
