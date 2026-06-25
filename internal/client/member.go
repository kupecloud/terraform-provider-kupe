package client

import (
	"context"
	"net/http"
	"strings"
)

// Member represents a tenant member.
type Member struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// AddMemberRequest is the body for adding a member.
type AddMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// UpdateMemberRequest is the body for updating a member's role.
type UpdateMemberRequest struct {
	Role string `json:"role"`
}

// ListMembers lists all members of the tenant.
func (c *Client) ListMembers(ctx context.Context) ([]Member, error) {
	var resp struct {
		Items []Member `json:"items"`
	}
	_, err := c.request(ctx, http.MethodGet, c.tenantPath("members"), nil, &resp)
	return resp.Items, err
}

// AddMember adds a new member to the tenant.
func (c *Client) AddMember(ctx context.Context, req AddMemberRequest) (*Member, error) {
	var member Member
	_, err := c.request(ctx, http.MethodPost, c.tenantPath("members"), req, &member)
	if err != nil {
		return nil, err
	}
	return &member, nil
}

// UpdateMember updates a member's role. The email is lowercased before
// building the path because kupe-api keys members by the canonical
// lowercased address (AddMember normalises on create). Terraform state
// preserves the user's original casing, so a mixed-case email like
// "User@Acme.com" would otherwise PATCH /members/User@Acme.com and 404.
func (c *Client) UpdateMember(ctx context.Context, email string, req UpdateMemberRequest) (*Member, error) {
	var member Member
	_, err := c.request(ctx, http.MethodPatch, c.tenantPath("members", strings.ToLower(email)), req, &member)
	if err != nil {
		return nil, err
	}
	return &member, nil
}

// RemoveMember removes a member from the tenant. The email is lowercased
// for the same reason as UpdateMember — kupe-api keys members by the
// canonical lowercased address, so a mixed-case state email would 404 on
// destroy.
func (c *Client) RemoveMember(ctx context.Context, email string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("members", strings.ToLower(email)), nil, nil)
	return err
}
