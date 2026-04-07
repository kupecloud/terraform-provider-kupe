package client

import (
	"context"
	"net/http"
	"net/url"
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

// UpdateMember updates a member's role.
func (c *Client) UpdateMember(ctx context.Context, email string, req UpdateMemberRequest) (*Member, error) {
	var member Member
	_, err := c.request(ctx, http.MethodPatch, c.tenantPath("members", url.PathEscape(email)), req, &member)
	if err != nil {
		return nil, err
	}
	return &member, nil
}

// RemoveMember removes a member from the tenant.
func (c *Client) RemoveMember(ctx context.Context, email string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("members", url.PathEscape(email)), nil, nil)
	return err
}
