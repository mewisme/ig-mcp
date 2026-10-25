package instagram

import (
	"testing"

	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func TestAccountFromValidatedViewer(t *testing.T) {
	viewer := &types.PolarisViewer{
		Data: types.ViewerData{
			ID:       "100234",
			Username: "example.user",
			FullName: "Example\x1b[31m User\nName",
		},
	}
	account := accountFromViewer(viewer, "fallback-id")
	if account.ID != "100234" || account.Username != "example.user" || account.FullName != "Example [31m User Name" {
		t.Fatalf("unexpected sanitized identity: %+v", account)
	}
	if got := accountFromViewer(nil, "234"); got != (AccountInfo{ID: "234"}) {
		t.Fatalf("nil viewer should preserve cookie ID: %+v", got)
	}
	viewer.ID = "456"
	if got := accountFromViewer(viewer, "fallback-id"); got.ID != "456" {
		t.Fatalf("outer viewer ID should take precedence: %+v", got)
	}
}

func TestVerifiedAccountOnlyWhenReady(t *testing.T) {
	s := New()
	if _, ok := s.VerifiedAccount(); ok {
		t.Fatal("new service must not report a verified account")
	}
	s.mu.Lock()
	s.status = "ready"
	s.account = AccountInfo{ID: "123", Username: "verified"}
	s.mu.Unlock()
	account, ok := s.VerifiedAccount()
	if !ok || account.Username != "verified" {
		t.Fatal("ready service should expose validated identity")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifiedAccount(); ok {
		t.Fatal("closed service must not report an active verified account")
	}
}
