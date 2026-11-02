package provider

import (
	"context"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var appEnvNamePattern = regexp.MustCompile(`^[-._a-zA-Z][-._a-zA-Z0-9]*$`)
var appVaultPathPattern = regexp.MustCompile(`^[a-zA-Z0-9]([-a-zA-Z0-9_./]*[a-zA-Z0-9])?$`)
var appDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// ValidateConfig checks cross-field constraints without treating unknown
// references as empty. The API remains authoritative for live ownership/caps.
func (r *AppEnvironmentResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config AppEnvironmentResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rejectNullAppListEntries(config.Secrets, path.Root("secrets"), &resp.Diagnostics)
	declared := map[string]bool{}
	declarationsKnown := !config.Secrets.IsUnknown()
	for i, item := range config.Secrets.Elements() {
		if item.IsNull() {
			continue
		}
		if item.IsUnknown() {
			declarationsKnown = false
			continue
		}
		attrs := item.(types.Object).Attributes()
		name, vaultPath := attrs["name"].(types.String), attrs["path"].(types.String)
		if name.IsUnknown() {
			declarationsKnown = false
		} else {
			if declared[name.ValueString()] {
				resp.Diagnostics.AddAttributeError(path.Root("secrets").AtListIndex(i).AtName("name"), "Duplicate secret", "Secret names must be unique.")
			}
			declared[name.ValueString()] = true
			if !config.App.IsUnknown() && !config.Name.IsUnknown() && len("app-"+config.App.ValueString()+"-"+config.Name.ValueString()+"-"+name.ValueString()) > 63 {
				resp.Diagnostics.AddAttributeError(path.Root("secrets").AtListIndex(i).AtName("name"), "Secret name too long", "The app, environment and secret name together must fit in the delivered secret's 63-character name.")
			}
		}
		if !vaultPath.IsUnknown() && (!appVaultPathPattern.MatchString(vaultPath.ValueString()) || strings.Contains(vaultPath.ValueString(), "..")) {
			resp.Diagnostics.AddAttributeError(path.Root("secrets").AtListIndex(i).AtName("path"), "Invalid Vault path", "Use a Vault path beginning with a letter or digit and ending with a letter or digit and containing only letters, digits, '/', '_', '.' and '-'; '..' is not allowed.")
		}
	}
	for _, item := range config.Domains.Elements() {
		if item.IsUnknown() {
			continue
		}
		domain := item.(types.String).ValueString()
		if len(domain) > 253 || !appDomainPattern.MatchString(domain) {
			resp.Diagnostics.AddAttributeError(path.Root("domains"), "Invalid domain", "Domains must be exact lowercase hostnames with at least two labels, at most 253 characters in total and at most 63 per label.")
		}
	}
	if config.Web.IsNull() || config.Web.IsUnknown() {
		return
	}
	web := config.Web.Attributes()
	for _, key := range []string{"command", "args"} {
		for i, item := range web[key].(types.List).Elements() {
			if !item.IsUnknown() && len(item.(types.String).ValueString()) > 1024 {
				resp.Diagnostics.AddAttributeError(path.Root("web").AtName(key).AtListIndex(i), "Argument too long", "Command and argument entries must be at most 1024 bytes.")
			}
		}
	}
	rejectNullAppListEntries(web["env"].(types.List), path.Root("web").AtName("env"), &resp.Diagnostics)
	names := map[string]bool{}
	for i, item := range web["env"].(types.List).Elements() {
		if item.IsNull() {
			continue
		}
		if item.IsUnknown() {
			continue
		}
		attrs := item.(types.Object).Attributes()
		name, value, ref := attrs["name"].(types.String), attrs["value"].(types.String), attrs["secret_key_ref"].(types.Object)
		base := path.Root("web").AtName("env").AtListIndex(i)
		if !name.IsUnknown() {
			text := name.ValueString()
			if len(text) > 253 || !appEnvNamePattern.MatchString(text) || strings.HasPrefix(text, "..") || text == "." || names[text] {
				resp.Diagnostics.AddAttributeError(base.AtName("name"), "Invalid environment variable", "Environment variable names must be valid, unique and at most 253 characters.")
			}
			names[text] = true
		}
		if !value.IsUnknown() && !ref.IsUnknown() && value.IsNull() == ref.IsNull() {
			resp.Diagnostics.AddAttributeError(base, "Choose one value source", "Exactly one of value or secret_key_ref must be set.")
		}
		if !value.IsUnknown() && utf8.RuneCountInString(value.ValueString()) > 4096 {
			resp.Diagnostics.AddAttributeError(base.AtName("value"), "Value too long", "Environment variable values must be at most 4096 characters.")
		}
		if !ref.IsNull() && !ref.IsUnknown() {
			secret := ref.Attributes()["secret"].(types.String)
			if declarationsKnown && !secret.IsUnknown() && !declared[secret.ValueString()] {
				resp.Diagnostics.AddAttributeError(base.AtName("secret_key_ref").AtName("secret"), "Secret not declared", "Add the referenced secret name and Vault path to secrets.")
			}
		}
	}
	if checks := web["health_checks"].(types.Object); !checks.IsNull() && !checks.IsUnknown() {
		for _, key := range []string{"readiness", "liveness"} {
			probe := checks.Attributes()[key].(types.Object)
			if probe.IsNull() || probe.IsUnknown() {
				continue
			}
			tcp, http := probe.Attributes()["tcp"].(types.Object), probe.Attributes()["http"].(types.Object)
			base := path.Root("web").AtName("health_checks").AtName(key)
			if !tcp.IsUnknown() && !http.IsUnknown() && tcp.IsNull() == http.IsNull() {
				resp.Diagnostics.AddAttributeError(base, "Choose one probe", "Exactly one of tcp or http must be set.")
			}
			if !http.IsNull() && !http.IsUnknown() {
				value := http.Attributes()["path"].(types.String)
				if !value.IsUnknown() && (!strings.HasPrefix(value.ValueString(), "/") || utf8.RuneCountInString(value.ValueString()) > 2048) {
					resp.Diagnostics.AddAttributeError(base.AtName("http").AtName("path"), "Invalid health check path", "HTTP health check paths must begin with / and be at most 2048 characters.")
				}
			}
		}
	}
	if resources := web["resources"].(types.Object); !resources.IsNull() && !resources.IsUnknown() {
		requests, limits := resources.Attributes()["requests"].(types.Object), resources.Attributes()["limits"].(types.Object)
		if requests.IsNull() || requests.IsUnknown() || limits.IsNull() || limits.IsUnknown() {
			return
		}
		for _, key := range []string{"cpu", "memory"} {
			request, limit := requests.Attributes()[key].(types.String), limits.Attributes()[key].(types.String)
			if request.IsNull() || request.IsUnknown() || limit.IsNull() || limit.IsUnknown() {
				continue
			}
			parse := parseSizeQuantity
			if key == "cpu" {
				parse = parseAppCPUQuantity
			}
			a, aOK := parse(request.ValueString())
			b, bOK := parse(limit.ValueString())
			if aOK && bOK && b.Cmp(a) < 0 {
				resp.Diagnostics.AddAttributeError(path.Root("web").AtName("resources").AtName("limits").AtName(key), "Limit below request", "Resource limits must be at least requests.")
			}
		}
	}
}

func parseAppCPUQuantity(value string) (*big.Rat, bool) {
	if !cpuQuantityPattern.MatchString(value) {
		return nil, false
	}
	quantity, ok := new(big.Rat).SetString(strings.TrimSuffix(value, "m"))
	if ok && strings.HasSuffix(value, "m") {
		quantity.Quo(quantity, big.NewRat(1000, 1))
	}
	return quantity, ok
}

// Resource validation runs before nested schema validation. Null objects have
// no attributes; reject them at their list index before traversing fields.
func rejectNullAppListEntries(list types.List, base path.Path, diags *diag.Diagnostics) {
	for i, value := range list.Elements() {
		if value.IsNull() {
			diags.AddAttributeError(base.AtListIndex(i), "Invalid nested entry", "List entries must be non-null objects.")
		}
	}
}
