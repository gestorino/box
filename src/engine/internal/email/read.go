package email

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"
)

const (
	reconcileEvery   = time.Minute
	reconcileMobile  = 15 * time.Minute // hostd says the uplink is the SIM: not a login a minute
	reconcileAuth    = 15 * time.Minute // after a refused login, never sooner
	reconcileTimeout = 2 * time.Minute  // a whole reconcile, whatever the connection does
)

// Unread counts the account's threads holding an unread incoming email. What the owner sent is never
// unread.
func (a *Archive) Unread(ctx context.Context, account string) (int, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT thread) FROM emails WHERE account = ? AND unread = 1`,
		account).Scan(&n)
	return n, err
}

// MarkThreadRead is the owner opening a thread: its incoming emails are read here at once and queued
// for Gmail, whether or not any was counted — opening a Promotions thread reads it, as in Gmail.
func (a *Archive) MarkThreadRead(ctx context.Context, account, thread string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE emails SET unread = 0, mark_pending = 1
		WHERE account = ? AND thread = ? AND direction = 'in'`, account, thread)
	return err
}

// pendingMarksSQL repeats the predicate of the emails_pending partial index and names it: the
// planner would otherwise take emails_uid's or emails_stored's account prefix over it.
const pendingMarksSQL = `SELECT uid FROM emails INDEXED BY emails_pending
	WHERE account = ? AND mark_pending = 1 AND direction = 'in' AND +uid > 0`

// PendingMarks are the UIDs of the account's emails waiting to be set \Seen on Gmail. A mark queued
// under another account stays behind, and one whose email has no UID (after a renumbering, until it
// is stored again) waits.
func (a *Archive) PendingMarks(ctx context.Context, account string) ([]uint32, error) {
	rows, err := a.db.QueryContext(ctx, pendingMarksSQL, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var u uint32
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	slices.Sort(out) // in UID order, not the index's
	return out, rows.Err()
}

// ApplyReconcile takes Gmail's answer: marked were set \Seen there, so they are no longer pending;
// the archived incoming emails among unread are the unread ones. Only the rows that change are
// written (emails holds the raw bodies). A row still pending — a mark made while the reconcile ran,
// which Gmail's answer predates — stays read, and so does one just marked, which Gmail's search may
// still list: it counts again on a later round only if Gmail still reports it.
func (a *Archive) ApplyReconcile(ctx context.Context, account string, marked, unread []uint32) error {
	done, err := json.Marshal(append([]uint32{}, marked...))
	if err != nil {
		return err
	}
	set, err := json.Marshal(append([]uint32{}, unread...))
	if err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE emails SET unread = 0
		WHERE account = ? AND direction = 'in' AND uid > 0 AND unread = 1
		AND (uid NOT IN (SELECT value FROM json_each(?)) OR uid IN (SELECT value FROM json_each(?)) OR mark_pending = 1)`,
		account, string(set), string(done)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE emails SET unread = 1
		WHERE account = ? AND direction = 'in' AND uid > 0 AND unread = 0 AND mark_pending = 0
		AND uid IN (SELECT value FROM json_each(?)) AND uid NOT IN (SELECT value FROM json_each(?))`,
		account, string(set), string(done)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE emails SET mark_pending = 0
		WHERE account = ? AND mark_pending = 1 AND uid > 0 AND uid IN (SELECT value FROM json_each(?))`,
		account, string(done)); err != nil {
		return err
	}
	return tx.Commit()
}

// Unread counts the threads of the account the reads serve that hold mail the owner has not opened.
func (s *Service) Unread(ctx context.Context) (int, error) {
	account := s.Status().Address
	if account == "" {
		return 0, nil
	}
	return s.a.Unread(ctx, account)
}

// MarkRead is the owner opening a thread: it is read here at once, and Gmail is told in the
// background — never inside the request, so a half-dead connection holds nothing up.
func (s *Service) MarkRead(ctx context.Context, thread string) error {
	account := s.Status().Address
	if account == "" {
		return nil
	}
	if err := s.a.MarkThreadRead(ctx, account, thread); err != nil {
		return err
	}
	select {
	case s.nudge <- struct{}{}:
	default:
	}
	return nil
}

// reconcileOnce sets \Seen on what the owner opened, then takes Gmail's unread set. It does nothing
// until the archive session has logged in. A failure changes nothing: the last set stays, and it is
// not an outage of the mailbox.
func (s *Service) reconcileOnce(ctx context.Context) error {
	s.mu.Lock()
	acct, state := s.acct, s.status.State
	s.mu.Unlock()
	if acct == nil || state != "connected" && state != "copying" && state != "paused_mobile" {
		return nil
	}
	// The stored account, not s.acct: the cursor there changes under the archive session's lock.
	stored, err := s.a.Account(ctx, acct.Address)
	if err != nil || stored == nil {
		return err
	}
	password, err := s.password(ctx)
	if err != nil {
		return err
	}
	marks, err := s.a.PendingMarks(ctx, stored.Address)
	if err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	unread, err := s.reconcile(call, stored.Address, password, stored.UIDValidity, marks)
	if err != nil {
		return err
	}
	return s.a.ApplyReconcile(ctx, stored.Address, marks, unread)
}

// reconcileWait is how long until the next reconcile.
func (s *Service) reconcileWait(ctx context.Context) time.Duration {
	if up, err := s.uplink(ctx); err == nil && up == "mobile" {
		return reconcileMobile
	}
	return reconcileEvery
}

// nextWait is how long to wait after a reconcile that ended in err, and whether an opened thread may
// end the wait early. A refused login waits out reconcileAuth, nudges ignored: the archive session
// owns auth_failed, and asking again soon would lock the account.
func (s *Service) nextWait(ctx context.Context, err error) (time.Duration, bool) {
	if errors.Is(err, ErrAuth) {
		return reconcileAuth, false
	}
	return s.reconcileWait(ctx), true
}

// reconcileLoop reconciles every minute (every 15 on mobile data) and at once when the owner opens a
// thread, until ctx ends. Opening waits for its pace on mobile data, like everything else here.
func (s *Service) reconcileLoop(ctx context.Context) {
	for {
		err := s.reconcileOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("email: reconcile failed, keeping the last unread count", "err", err)
		}
		wait, nudgeable := s.nextWait(ctx, err)
		if !s.sleep(ctx, wait, nudgeable) {
			return
		}
	}
}

// sleep waits out wait — or, when nudgeable, ends early at a nudge unless the uplink is mobile. It
// reports false when ctx ended.
func (s *Service) sleep(ctx context.Context, wait time.Duration, nudgeable bool) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-s.nudge:
			if nudgeable && s.reconcileWait(ctx) != reconcileMobile {
				return true
			}
		}
	}
}
