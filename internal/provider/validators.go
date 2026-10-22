package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// Name formats the platform enforces. "--" is reserved as a delimiter in the
// names the platform derives from databases and clusters, so those names
// may not contain it: each "-" must be followed by a letter or digit.
var (
	serviceNamePattern = regexp.MustCompile(`^[a-z](-?[a-z0-9])*$`)
	clusterRefPattern  = regexp.MustCompile(`^[a-z0-9](-?[a-z0-9])*$`)
	namespacePattern   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

// maxDatabaseNameLength is the longest name the platform gives a new
// database whatever the tenant: CloudNativePG derives object names from it,
// the longest {name}-{serial}-major-upgrade, which must fit in 63 characters.
const maxDatabaseNameLength = 44

// databaseNameValidators check a new database's name the way the platform's
// admission does: a DNS-1035 label (starts with a letter) without "--", at
// most 44 characters. The platform lowers the limit to 56 minus the length
// of the tenant name for tenant names over 12 characters; that needs the
// tenant's name, which the provider does not know at plan time, so kupe-api
// enforces it (DATABASE_NAME_TOO_LONG).
func databaseNameValidators() []validator.String {
	return serviceNameValidators(maxDatabaseNameLength)
}

// databaseRefValidators check a reference to a database by name. A database
// created before the platform's 44-character limit may be longer, so only
// the Kubernetes name limit applies.
func databaseRefValidators() []validator.String {
	return serviceNameValidators(maxNameLength)
}

// clusterNameValidators check a new cluster's name the way the platform's
// admission does: a DNS-1035 label (starts with a letter) without "--". The
// platform also caps the tenant and cluster names together (the namespace it
// derives from them must fit a DNS label); that needs the tenant's name, so
// it is left to the platform.
func clusterNameValidators() []validator.String {
	return serviceNameValidators(maxNameLength)
}

// serviceNameValidators check the name of a managed service resource, at
// most maxLength characters.
func serviceNameValidators(maxLength int) []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(maxLength),
		stringvalidator.RegexMatches(serviceNamePattern,
			"must be lowercase letters, digits and '-', start with a letter, end with a letter or digit, and not contain '--'"),
	}
}

// clusterRefValidators check a reference to a cluster by name.
func clusterRefValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(maxNameLength),
		stringvalidator.RegexMatches(clusterRefPattern,
			"must be lowercase letters, digits and '-', start and end with a letter or digit, and not contain '--'"),
	}
}

// bindingNamespaceValidators check the cluster namespace a binding delivers
// into: a DNS-1123 label that is not reserved for the platform.
func bindingNamespaceValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(maxNameLength),
		stringvalidator.RegexMatches(namespacePattern,
			"must be lowercase letters, digits and '-', and start and end with a letter or digit"),
		notPlatformNamespace{},
	}
}

// cpuQuantityValidators and sizeQuantityValidators check database resource
// quantities against the formats in quantity.go.
func cpuQuantityValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(maxQuantityLength),
		stringvalidator.RegexMatches(cpuQuantityPattern, `must be a CPU count without a unit, e.g. "2", "500m" or "0.5"`),
	}
}

func sizeQuantityValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(maxQuantityLength),
		stringvalidator.RegexMatches(sizeQuantityPattern,
			`must be a number with a unit suffix (Ki, Mi, Gi, Ti, Pi, Ei, k, M, G, T, P or E), e.g. "512Mi" or "10Gi"`),
	}
}

// notPlatformNamespace rejects the namespaces the platform reserves for
// itself inside every cluster: kube-* and argocd.
type notPlatformNamespace struct{}

func (v notPlatformNamespace) Description(_ context.Context) string {
	return "must not be a kube-* namespace or argocd"
}

func (v notPlatformNamespace) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v notPlatformNamespace) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	ns := req.ConfigValue.ValueString()
	if strings.HasPrefix(ns, "kube-") || ns == "argocd" {
		resp.Diagnostics.AddAttributeError(req.Path, "Reserved namespace",
			fmt.Sprintf("%s is %q: kube-* namespaces and argocd are reserved for the Kupe platform. Use an application namespace.",
				req.Path, ns))
	}
}

// registryName is the one name a tenant's registry has (kupe-control-operator
// domain.RegistryName; the ManagedRegistry CRD's name rule).
const registryName = "registry"

// registryNameValidators accept only the registry's one name, with the
// platform's own sentence for anything else.
func registryNameValidators() []validator.String {
	return []validator.String{fixedValue{value: registryName, why: "a tenant has one registry, named registry"}}
}

// fixedValue accepts one value only: an attribute the API fixes but that
// configuration may still name, such as the registry's name.
type fixedValue struct {
	value, why string
}

func (v fixedValue) Description(_ context.Context) string {
	return fmt.Sprintf("must be %q", v.value)
}

func (v fixedValue) MarkdownDescription(_ context.Context) string {
	return fmt.Sprintf("must be `%s`", v.value)
}

func (v fixedValue) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() == v.value {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+req.Path.String(),
		fmt.Sprintf("%s is %q: %s. Use %q or leave it out.", req.Path, req.ConfigValue.ValueString(), v.why, v.value))
}

// registryStorageLimitPattern is the ManagedRegistry CRD's spec.storageLimit
// pattern: binary units only (Mi, Gi, Ti), so "1" can never mean one byte. It
// is narrower than sizeQuantityPattern, which databases use.
var registryStorageLimitPattern = regexp.MustCompile(`^(\d+(\.\d+)?)(Mi|Gi|Ti)$`)

// registryStorageLimitValidators check a registry storage limit the way the
// platform does: a binary-unit quantity of at most 20 characters, more than
// zero. The plan's cap is checked by the platform
// (REGISTRY_STORAGE_LIMIT_ABOVE_PLAN).
func registryStorageLimitValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(maxQuantityLength),
		stringvalidator.RegexMatches(registryStorageLimitPattern,
			`must be a number with a binary unit (Mi, Gi or Ti), e.g. "10Gi"`),
		stringvalidator.RegexMatches(regexp.MustCompile(`[1-9]`), "must be more than zero"),
	}
}
