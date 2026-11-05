package secrets

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUIDIsOurs(t *testing.T) {
	dir, err := os.MkdirTemp("", "peer")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); done <- c }()
	cl, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	srv := <-done
	defer srv.Close()
	uid, err := PeerUID(srv)
	if err != nil {
		t.Fatal(err)
	}
	if int(uid) != os.Getuid() {
		t.Fatalf("uid %d, want %d", uid, os.Getuid())
	}
}

func TestPeerUIDNotUnix(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := PeerUID(a); err == nil {
		t.Fatal("a pipe has no peer uid")
	}
}
