package calls

import (
	"context"
	"io"
)

// Duplex is the call's sound both ways while it is routed: in yields the other party's voice and
// out takes what they hear, both S16_LE mono at 8 kHz. Closing either ends that direction.
type Duplex interface {
	Open(ctx context.Context) (in io.ReadCloser, out io.WriteCloser, err error)
}

// Audio is the call's sound: Route sends the call's voice to the modem's USB sound card, Unroute
// puts it back.
type Audio interface {
	Route(ctx context.Context) error
	Unroute(ctx context.Context) error
}
