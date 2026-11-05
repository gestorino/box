package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/updates"
)

// SessionStore is gotd's session storage. Until Persist it holds the session in memory only, so a
// half-signed-in key (QR scanned, password not given yet) never reaches disk; after Persist every
// write goes through to telegram.db.
type SessionStore struct {
	s *Store

	mu        sync.Mutex
	data      []byte
	persisted bool
}

func NewSessionStore(s *Store) *SessionStore {
	ss := &SessionStore{s: s}
	if v, err := s.Get(context.Background(), "session"); err == nil && v != "" {
		ss.data, ss.persisted = []byte(v), true
	}
	return ss
}

func (ss *SessionStore) LoadSession(context.Context) ([]byte, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if len(ss.data) == 0 {
		return nil, session.ErrNotFound
	}
	return append([]byte(nil), ss.data...), nil
}

func (ss *SessionStore) StoreSession(ctx context.Context, data []byte) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.data = append([]byte(nil), data...)
	if !ss.persisted {
		return nil
	}
	return ss.s.Set(ctx, "session", string(ss.data))
}

// Persist is called once the account is linked.
func (ss *SessionStore) Persist(ctx context.Context) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if err := ss.s.Set(ctx, "session", string(ss.data)); err != nil {
		return err
	}
	ss.persisted = true
	return nil
}

// Linked: a session was persisted, so Telegram signing the box out means the session was ended.
func (ss *SessionStore) Linked() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.persisted
}

// Forget drops the session from memory and disk; the next pairing starts memory-only again.
func (ss *SessionStore) Forget(ctx context.Context) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.data, ss.persisted = nil, false
	return ss.s.Set(ctx, "session", "")
}

// UpdateState is gotd's update-manager storage: the account's common sequence (pts, qts, date,
// seq) in config, each supergroup's and channel's pts in channel_state, access hashes in peers.
type UpdateState struct {
	s          *Store
	mu         sync.Mutex  // read-modify-write of the common state
	broadcasts atomic.Bool // Archive channels: broadcast channels' state is kept
}

func NewUpdateState(s *Store) *UpdateState { return &UpdateState{s: s} }

// TrackBroadcasts follows the Archive channels switch: while off, a broadcast channel's pts is
// neither written nor handed back to gotd at start, so gotd does not poll it after a restart.
func (u *UpdateState) TrackBroadcasts(on bool) { u.broadcasts.Store(on) }

func (u *UpdateState) skipped(ctx context.Context, userID, channelID int64) bool {
	if u.broadcasts.Load() {
		return false
	}
	p, _, _ := u.s.Peer(ctx, acct(userID), fmt.Sprintf("channel:%d", channelID))
	return isBroadcast(p)
}

func acct(userID int64) string { return strconv.FormatInt(userID, 10) }

func stateKey(userID int64) string { return "update_state:" + acct(userID) }

func (u *UpdateState) GetState(ctx context.Context, userID int64) (updates.State, bool, error) {
	v, err := u.s.Get(ctx, stateKey(userID))
	if err != nil || v == "" {
		return updates.State{}, false, err
	}
	var st updates.State
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return updates.State{}, false, fmt.Errorf("update state: %w", err)
	}
	return st, true, nil
}

func (u *UpdateState) SetState(ctx context.Context, userID int64, st updates.State) error {
	b, _ := json.Marshal(st)
	return u.s.Set(ctx, stateKey(userID), string(b))
}

func (u *UpdateState) change(ctx context.Context, userID int64, f func(*updates.State)) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, _, err := u.GetState(ctx, userID)
	if err != nil {
		return err
	}
	f(&st)
	return u.SetState(ctx, userID, st)
}

func (u *UpdateState) SetPts(ctx context.Context, userID int64, pts int) error {
	return u.change(ctx, userID, func(s *updates.State) { s.Pts = pts })
}

func (u *UpdateState) SetQts(ctx context.Context, userID int64, qts int) error {
	return u.change(ctx, userID, func(s *updates.State) { s.Qts = qts })
}

func (u *UpdateState) SetDate(ctx context.Context, userID int64, date int) error {
	return u.change(ctx, userID, func(s *updates.State) { s.Date = date })
}

func (u *UpdateState) SetSeq(ctx context.Context, userID int64, seq int) error {
	return u.change(ctx, userID, func(s *updates.State) { s.Seq = seq })
}

func (u *UpdateState) SetDateSeq(ctx context.Context, userID int64, date, seq int) error {
	return u.change(ctx, userID, func(s *updates.State) { s.Date, s.Seq = date, seq })
}

func (u *UpdateState) GetChannelPts(ctx context.Context, userID, channelID int64) (int, bool, error) {
	var pts int
	err := u.s.db.QueryRowContext(ctx, `SELECT pts FROM channel_state WHERE account = ? AND channel_id = ?`,
		acct(userID), channelID).Scan(&pts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return pts, err == nil, err
}

func (u *UpdateState) SetChannelPts(ctx context.Context, userID, channelID int64, pts int) error {
	if u.skipped(ctx, userID, channelID) {
		return nil
	}
	_, err := u.s.db.ExecContext(ctx, `INSERT INTO channel_state (account, channel_id, pts) VALUES (?,?,?)
		ON CONFLICT (account, channel_id) DO UPDATE SET pts = excluded.pts`, acct(userID), channelID, pts)
	return err
}

func (u *UpdateState) ForEachChannels(ctx context.Context, userID int64, f func(ctx context.Context, channelID int64, pts int) error) error {
	rows, err := u.s.db.QueryContext(ctx, `SELECT channel_id, pts FROM channel_state WHERE account = ?`, acct(userID))
	if err != nil {
		return err
	}
	type ch struct {
		id  int64
		pts int
	}
	var all []ch
	for rows.Next() {
		var c ch
		if err := rows.Scan(&c.id, &c.pts); err != nil {
			rows.Close()
			return err
		}
		all = append(all, c)
	}
	rows.Close() // one connection: release it before f touches the store
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range all {
		if u.skipped(ctx, userID, c.id) {
			continue
		}
		if err := f(ctx, c.id, c.pts); err != nil {
			return err
		}
	}
	return nil
}

// DropChannels stops tracking the given channels (Archive channels turned off).
func (u *UpdateState) DropChannels(ctx context.Context, account string, ids []int64) error {
	for _, id := range ids {
		if _, err := u.s.db.ExecContext(ctx, `DELETE FROM channel_state WHERE account = ? AND channel_id = ?`, account, id); err != nil {
			return err
		}
	}
	return nil
}

func (u *UpdateState) hash(ctx context.Context, userID int64, key string) (int64, bool, error) {
	p, ok, err := u.s.Peer(ctx, acct(userID), key)
	if err != nil || !ok || p.AccessHash == 0 {
		return 0, false, err
	}
	return p.AccessHash, true, nil
}

func (u *UpdateState) SetChannelAccessHash(ctx context.Context, userID, channelID, accessHash int64) error {
	return u.s.PutPeer(ctx, Peer{Account: acct(userID), Key: fmt.Sprintf("channel:%d", channelID), AccessHash: accessHash})
}

func (u *UpdateState) GetChannelAccessHash(ctx context.Context, userID, channelID int64) (int64, bool, error) {
	return u.hash(ctx, userID, fmt.Sprintf("channel:%d", channelID))
}

func (u *UpdateState) SetUserAccessHash(ctx context.Context, userID, targetUserID, accessHash int64) error {
	return u.s.PutPeer(ctx, Peer{Account: acct(userID), Key: fmt.Sprintf("user:%d", targetUserID), AccessHash: accessHash})
}

func (u *UpdateState) GetUserAccessHash(ctx context.Context, userID, targetUserID int64) (int64, bool, error) {
	return u.hash(ctx, userID, fmt.Sprintf("user:%d", targetUserID))
}
