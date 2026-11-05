package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/gotd/td/tg"
)

// Creds are the owner's app credentials from my.telegram.org.
type Creds struct {
	ID   int
	Hash string
}

// Self is the linked account.
type Self struct {
	ID    int64  `json:"id"`
	Phone string `json:"phone"`
	Name  string `json:"name"`
}

var (
	ErrState          = errors.New("not possible in this state")
	ErrLogout         = errors.New("couldn't reach Telegram to log out") // the owner may then remove anyway
	ErrCredentials    = errors.New("Telegram refused the api_id or api_hash")
	ErrPassword       = errors.New("Wrong password")
	ErrPasswordNeeded = errors.New("password needed")
	ErrNotConnected   = errors.New("not connected") // a send needs the session up
)

// Client is everything the service asks of gotd; gotd.go implements it, tests fake it.
type Client interface {
	// Check proves a pair of credentials on a throwaway connection; ErrCredentials when refused.
	Check(ctx context.Context, c Creds) error
	// Run connects with the stored session and blocks, reconnecting by itself, until ctx ends or
	// Telegram answers 401 (the error then satisfies auth.IsUnauthorized). Updates arrive as events.
	Run(ctx context.Context, c Creds, events func(any)) error
	// QR logs in by QR code within Run; show is called for every token. ErrPasswordNeeded when the
	// account has a cloud password.
	QR(ctx context.Context, show func(url string, expires time.Time)) (Self, error)
	// Password finishes a QR login that needs the cloud password; ErrPassword when wrong.
	Password(ctx context.Context, pw string) (Self, error)
	// Proceed lets a Run waiting signed-out start receiving updates, once the service has stored
	// the link a QR or Password call returned — never before, or Connected could beat it.
	Proceed()
	LogOut(ctx context.Context) error
	Offline(ctx context.Context) error
	// Dialogs pages through every dialog, sleeping on FLOOD_WAIT.
	Dialogs(ctx context.Context, page func(Dialog) error) error
	// ReadHistory tells Telegram the owner read a private chat up to message maxID.
	ReadHistory(ctx context.Context, peer InputPeer, maxID int) error
	History(ctx context.Context, peer InputPeer, limit int) ([]tg.MessageClass, tg.Entities, error)
	// Send sends a text to peer (Key "self": Saved Messages); id and at are Telegram's.
	Send(ctx context.Context, peer InputPeer, text string) (id int, at time.Time, err error)
}

type InputPeer struct {
	Key        string
	AccessHash int64
}

// Dialog is one chat from messages.getDialogs. Pts is set for supergroups and channels.
type Dialog struct {
	Peer           Peer
	Pts            int
	ReadInboxMaxID int // the newest incoming message the owner has read, on any device
}

// Events from Run.
type (
	Connected  struct{} // signed in, updates flowing
	NewMessage struct {
		Msg tg.MessageClass
		Ent tg.Entities
	}
	EditMessage struct {
		Msg tg.MessageClass
		Ent tg.Entities
	}
	DeletedCommon  struct{ IDs []int }
	DeletedChannel struct {
		ChannelID int64
		IDs       []int
	}
	TooLong struct{ ChannelID int64 } // 0: the common sequence
	// ReadInbox: the owner read a private chat on another device, up to MaxID.
	ReadInbox struct {
		Peer  string
		MaxID int
	}
)
