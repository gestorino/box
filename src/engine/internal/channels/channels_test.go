package channels

import (
	"context"
	"reflect"
	"testing"
)

type stub string

func (s stub) Name() string                        { return string(s) }
func (s stub) Send(context.Context, Message) error { return nil }

func TestRegistryListsChannelsByName(t *testing.T) {
	r, err := NewRegistry(stub("sms"), stub("email"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Names(); !reflect.DeepEqual(got, []string{"email", "sms"}) {
		t.Fatalf("got %v", got)
	}
	if _, ok := r.Get("email"); !ok {
		t.Fatal("email not found")
	}
	if _, ok := r.Get("whatsapp"); ok {
		t.Fatal("whatsapp was never registered")
	}
}

func TestRegistryRefusesDuplicateNames(t *testing.T) {
	if _, err := NewRegistry(stub("email"), stub("email")); err == nil {
		t.Fatal("want an error for two channels named email")
	}
}
