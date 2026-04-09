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
