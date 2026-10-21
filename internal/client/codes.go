package client

import "errors"

// Error codes kupe-api returns in the error envelope for clusters,
// databases and the registry. They are stable: branch on them, not on the
// message, and compare them exactly (the platform's own codes are upper
// case, kupe-api's lower case). The operator's own canonical codes (e.g.
// HA_DISABLE_UNSUPPORTED) arrive the same way and are shown as they are.
const (
	// CodeDeletionProtected comes with a 409 when a database or registry
	// delete is refused because deletion protection is enabled.
	CodeDeletionProtected = "deletion_protected"
	// CodeDatabasesUnavailable comes with a 404 when managed databases are
	// not enabled on the platform. Reads, deletes, a database PATCH that
	// only changes deletionProtection and a binding PATCH that only removes
	// namespaces still work then, so existing databases and bindings can be
	// removed.
	CodeDatabasesUnavailable = "databases_unavailable"
	// CodeCreationPaused comes with a 400 when the platform has temporarily
	// paused creating clusters or databases. Existing resources are
	// unaffected.
	CodeCreationPaused = "creation_paused"
	// CodeMajorUpgradeUnavailable comes with a 400 when a PATCH changes the
	// engine major version.
	CodeMajorUpgradeUnavailable = "major_upgrade_unavailable"
	// CodeStorageShrinkUnsupported comes with a 400 when a PATCH lowers the
	// storage size.
	CodeStorageShrinkUnsupported = "storage_shrink_unsupported"
	// CodeTerminating comes with a 409 on a change to a cluster or database
	// that is being deleted, or a binding of a database that is.
	CodeTerminating = "terminating"
	// CodeTenantTerminating comes with a 409 when creating or growing a
	// cluster or database while the tenant is being deleted.
	CodeTenantTerminating = "tenant_terminating"
	// CodeTenantSuspended comes with a 409 when creating or growing a
	// cluster or database while the tenant is suspended.
	CodeTenantSuspended = "tenant_suspended"
	// CodePaymentSetupRequired comes with a 409 when creating or growing a
	// cluster or database before the tenant has added a payment method.
	CodePaymentSetupRequired = "payment_setup_required"
	// CodePlanLimitReached comes with a 400 when the tenant's plan has no
	// room for another cluster or database.
	CodePlanLimitReached = "plan_limit_reached"
	// CodeDatabaseNameTooLong comes with a 400 when a new database's name is
	// longer than the tenant may use: 44 characters, or 56 minus the length
	// of the tenant name when that is less. The platform's own code.
	CodeDatabaseNameTooLong = "DATABASE_NAME_TOO_LONG"
	// CodeBackupsUnavailable comes with a 400 when a create or PATCH sets
	// backup settings while the platform does not offer backups.
	CodeBackupsUnavailable = "backups_unavailable"
)

// Registry codes. kupe-api's own are lower case; the platform's (the
// operator's admission) are upper case and passed through unchanged.
const (
	// CodeRegistriesUnavailable comes with a 404 when the registry is not
	// enabled on the platform. Reads, deletes, a registry PATCH that only
	// changes deletionProtection and a cluster connection PATCH that only
	// removes push namespaces still work then, so an existing registry, its
	// connections and tokens can be removed.
	CodeRegistriesUnavailable = "registries_unavailable"
	// CodeRegistryAlreadyExists comes with a 409 when the tenant already has
	// its one registry.
	CodeRegistryAlreadyExists = "REGISTRY_ALREADY_EXISTS"
	// CodeRegistryStorageLimitAbovePlan comes with a 400 when a storage
	// limit is above the plan's cap, or the plan includes no registry.
	CodeRegistryStorageLimitAbovePlan = "REGISTRY_STORAGE_LIMIT_ABOVE_PLAN"
	// CodeRegistryBindingExists comes with a 409 when the cluster already
	// has a cluster connection.
	CodeRegistryBindingExists = "REGISTRY_BINDING_EXISTS"
	// CodeRegistryPushNamespacesLimit comes with a 400 when one cluster
	// connection lists more push namespaces than the platform allows.
	CodeRegistryPushNamespacesLimit = "REGISTRY_PUSH_NAMESPACES_LIMIT"
	// CodeRegistryPushNamespacesServiceLimit comes with a 400 when the push
	// namespaces of all the registry's cluster connections together would
	// exceed the platform's cap. The sum needs every connection, so the plan
	// cannot check it: it is refused at apply time.
	CodeRegistryPushNamespacesServiceLimit = "REGISTRY_PUSH_NAMESPACES_SERVICE_LIMIT"
	// CodeRegistryTokenPushAdminOnly comes with a 403 when a push access
	// token is created by a credential without the admin role.
	CodeRegistryTokenPushAdminOnly = "REGISTRY_TOKEN_PUSH_ADMIN_ONLY" // #nosec G101 -- an error code, not a credential
	// CodeRegistryTokenExpiryInvalid comes with a 400 when an access
	// token's lifetime is out of range.
	CodeRegistryTokenExpiryInvalid = "REGISTRY_TOKEN_EXPIRY_INVALID" // #nosec G101 -- an error code, not a credential
	// CodeRegistryTokenLimitReached comes with a 409 when the registry
	// already holds the most access tokens it may.
	CodeRegistryTokenLimitReached = "REGISTRY_TOKEN_LIMIT_REACHED" // #nosec G101 -- an error code, not a credential
)

// ErrorCode is the code of the API error err carries, "" when it carries
// none or is not an API error.
func ErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}
