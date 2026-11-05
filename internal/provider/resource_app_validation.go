package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// App names share the cluster-reference delimiter rule, but have tighter limits.
func appNameValidators(limit int) []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(limit),
		stringvalidator.RegexMatches(clusterRefPattern, "must be an RFC 1123 label without '--'"),
	}
}

// This is the shipping API/CRD grammar, including an explicit registry host.
var appDigestPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?((\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:[0-9]{1,5})?|:[0-9]{1,5})/[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*(/[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*)*@sha256:[a-f0-9]{64}$`)

type appImageDigest struct{}

func (v appImageDigest) Description(_ context.Context) string {
	return "must be an image reference pinned by sha256 digest"
}
func (v appImageDigest) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v appImageDigest) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if len(value) > 512 || !appDigestPattern.MatchString(value) {
		resp.Diagnostics.AddAttributeError(req.Path, "Image digest required", "web.image must include a registry host and a sha256 digest. Resolve a tag with `data.kupe_app_image` and use its `reference` before applying.")
	}
}
