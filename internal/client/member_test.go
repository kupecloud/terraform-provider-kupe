package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestListMembers(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/tenants/acme/members", http.StatusOK, map[string]any{
		"items": []Member{
			{Email: "admin@acme.com", Role: "admin"},
			{Email: "dev@acme.com", Role: "readonly"},
		},
	})

	c := mock.client("acme")
	members, err := c.ListMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2, got %d", len(members))
	}
	if members[0].Email != "admin@acme.com" {
		t.Errorf("expected admin@acme.com, got %q", members[0].Email)
	}
}

func TestAddMember(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("POST", "/api/v1/tenants/acme/members", http.StatusCreated, Member{
		Email: "new@acme.com",
		Role:  "readonly",
	})

	c := mock.client("acme")
	member, err := c.AddMember(context.Background(), AddMemberRequest{
		Email: "new@acme.com",
		Role:  "readonly",
	})
	if err != nil {
		t.Fatal(err)
	}
	if member.Email != "new@acme.com" {
		t.Errorf("expected new@acme.com, got %q", member.Email)
	}

	req := mock.lastRequest()
	var body AddMemberRequest
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}
	if body.Role != "readonly" {
		t.Errorf("expected role=readonly, got %q", body.Role)
	}
}

func TestUpdateMember(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	// URL-encoded email
	mock.onJSON("PATCH", "/api/v1/tenants/acme/members/dev@acme.com", http.StatusOK, Member{
		Email: "dev@acme.com",
		Role:  "admin",
	})

	c := mock.client("acme")
	member, err := c.UpdateMember(context.Background(), "dev@acme.com", UpdateMemberRequest{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if member.Role != "admin" {
		t.Errorf("expected admin, got %q", member.Role)
	}
}

func TestRemoveMember(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("DELETE", "/api/v1/tenants/acme/members/dev@acme.com", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	c := mock.client("acme")
	if err := c.RemoveMember(context.Background(), "dev@acme.com"); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateMember_LowercasesEmail guards TPK-2: kupe-api keys members by
// the canonical lowercased email (AddMember normalises on create), but the
// provider preserves the user's original casing in state. UpdateMember must
// lowercase the path segment so a role change on a mixed-case email doesn't
// PATCH a non-existent /members/User@Acme.com and 404.
func TestUpdateMember_LowercasesEmail(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("PATCH", "/api/v1/tenants/acme/members/user@acme.com", http.StatusOK, Member{
		Email: "user@acme.com",
		Role:  "admin",
	})

	c := mock.client("acme")
	member, err := c.UpdateMember(context.Background(), "User@Acme.com", UpdateMemberRequest{Role: "admin"})
	if err != nil {
		t.Fatalf("expected lowercased path to match, got error: %v", err)
	}
	if member.Role != "admin" {
		t.Errorf("expected admin, got %q", member.Role)
	}
	if got := mock.lastRequest().Path; got != "/api/v1/tenants/acme/members/user@acme.com" {
		t.Errorf("expected lowercased path, got %q", got)
	}
}

// TestRemoveMember_LowercasesEmail is the destroy-path counterpart to
// TestUpdateMember_LowercasesEmail (TPK-2).
func TestRemoveMember_LowercasesEmail(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("DELETE", "/api/v1/tenants/acme/members/user@acme.com", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	c := mock.client("acme")
	if err := c.RemoveMember(context.Background(), "User@Acme.com"); err != nil {
		t.Fatalf("expected lowercased path to match, got error: %v", err)
	}
	if got := mock.lastRequest().Path; got != "/api/v1/tenants/acme/members/user@acme.com" {
		t.Errorf("expected lowercased path, got %q", got)
	}
}
