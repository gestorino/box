package hostd

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const password = "hunter2hunter2"

// finished polls until the test leaves "running"; the test itself runs in hostd's own goroutine.
func finished(t *testing.T, s *Service, id string) TestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r, ok := s.TestResult(id)
		if !ok {
			t.Fatalf("unknown test %s", id)
		}
		if r.State != "running" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("test never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTestPassesAndAlwaysRollsBack(t *testing.T) {
	s, nm, _, _ := newService()
	nm.activation = Activation{Connected: true, IP4: "10.0.0.23"}
	id, err := s.StartTest(ctx, "cafe", password)
	if err != nil {
		t.Fatal(err)
	}
	if r := finished(t, s, id); r != (TestResult{State: "passed", IP4: "10.0.0.23"}) {
		t.Fatalf("result %+v", r)
	}
	want := []string{
		"CheckpointCreate(wifi,1m30s)",
		"AddWifi(cafe,mem=true,auto=false,prio=0)",
		"ActivateWifi(22222222-2222-2222-2222-222222222222,30s)",
		"CheckpointRollback(/cp/1)",
	}
	if got := nm.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestTestFailureReportsReason(t *testing.T) {
	s, nm, _, _ := newService()
	nm.activation = Activation{Reason: "wrong_password"}
	id, _ := s.StartTest(ctx, "cafe", password)
	if r := finished(t, s, id); r != (TestResult{State: "failed", Reason: "wrong_password"}) {
		t.Fatalf("result %+v", r)
	}
	if calls := nm.Calls(); calls[len(calls)-1] != "CheckpointRollback(/cp/1)" {
		t.Fatalf("a failed test must still roll back: %v", calls)
	}
}

func TestTestAdapterErrorIsFailedNotStuck(t *testing.T) {
	s, nm, _, _ := newService()
	nm.err["AddWifi"] = errors.New("dbus: boom")
	id, _ := s.StartTest(ctx, "cafe", password)
	if r := finished(t, s, id); r != (TestResult{State: "failed", Reason: "unavailable"}) {
		t.Fatalf("result %+v", r)
	}
	if calls := nm.Calls(); calls[len(calls)-1] != "CheckpointRollback(/cp/1)" {
		t.Fatalf("calls %v", calls)
	}
}

func TestChangeWhileTestRunningIsBusy(t *testing.T) {
	s, nm, _, _ := newService()
	nm.block = make(chan struct{})
	nm.activation = Activation{Connected: true}
	id, _ := s.StartTest(ctx, "cafe", password)
	if _, err := s.Connect(ctx, savedID); !errors.Is(err, ErrBusy) {
		t.Fatalf("connect during a test: want ErrBusy, got %v", err)
	}
	if _, err := s.StartTest(ctx, "other", password); !errors.Is(err, ErrBusy) {
		t.Fatalf("second test: want ErrBusy, got %v", err)
	}
	if s.Pending() != nil {
		t.Fatal("a running test is not a pending change the owner can keep")
	}
	close(nm.block)
	finished(t, s, id)

	if _, err := s.Connect(ctx, savedID); err != nil {
		t.Fatalf("after the test: %v", err)
	}
	if _, err := s.StartTest(ctx, "cafe", password); !errors.Is(err, ErrBusy) {
		t.Fatalf("test during a pending change: want ErrBusy, got %v", err)
	}
}

func TestSaveRequiresPassedTest(t *testing.T) {
	s, nm, _, clock := newService()
	if _, err := s.Save(ctx, "cafe", password); !errors.Is(err, ErrNotTested) {
		t.Fatalf("untested: want ErrNotTested, got %v", err)
	}

	nm.activation = Activation{Reason: "wrong_password"}
	finished(t, s, mustStart(t, s, "cafe", password))
	if _, err := s.Save(ctx, "cafe", password); !errors.Is(err, ErrNotTested) {
		t.Fatalf("after a failed test: want ErrNotTested, got %v", err)
	}

	nm.activation = Activation{Connected: true}
	finished(t, s, mustStart(t, s, "cafe", password))
	if _, err := s.Save(ctx, "cafe", "another-password"); !errors.Is(err, ErrNotTested) {
		t.Fatalf("different password: want ErrNotTested, got %v", err)
	}
	saved, err := s.Save(ctx, "cafe", password)
	if err != nil || saved.SSID != "cafe" || saved.ID == "" {
		t.Fatalf("save after a green test: %+v %v", saved, err)
	}
	if calls := nm.Calls(); calls[len(calls)-1] != "AddWifi(cafe,mem=false,auto=true,prio=-1)" {
		t.Fatalf("calls %v", calls)
	}

	finished(t, s, mustStart(t, s, "cafe", password))
	clock.advance(10*time.Minute + time.Second)
	if _, err := s.Save(ctx, "cafe", password); !errors.Is(err, ErrNotTested) {
		t.Fatalf("after 10 minutes: want ErrNotTested, got %v", err)
	}
}

// The service keeps only salted hashes of tested pairs, never the password itself.
func TestTestResultKeepsNoPassword(t *testing.T) {
	s, nm, _, _ := newService()
	nm.activation = Activation{Connected: true}
	finished(t, s, mustStart(t, s, "cafe", password))
	if dump := fmt.Sprintf("%+v", s); strings.Contains(dump, password) {
		t.Fatal("the service holds the password in memory")
	}
}

func mustStart(t *testing.T, s *Service, ssid, pw string) string {
	t.Helper()
	id, err := s.StartTest(ctx, ssid, pw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Every checkpoint deletes connections created after it (DELETE_NEW_CONNECTIONS): a save made
// while one is pending would vanish on its rollback, so Save waits its turn like a change.
func TestSaveWhileChangePendingIsBusy(t *testing.T) {
	s, nm, _, _ := newService()
	nm.activation = Activation{Connected: true}
	finished(t, s, mustStart(t, s, "cafe", password))
	if _, err := s.Connect(ctx, savedID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, "cafe", password); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
}
