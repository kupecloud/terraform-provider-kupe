package client

import "errors"

// Error codes kupe-api returns in the error envelope for clusters and
// databases. They are stable: branch on them, not on the message. The
// operator's own canonical codes (e.g. HA_DISABLE_UNSUPPORTED) arrive the
// same way and are shown as they are.
const (
	// CodeDeletionProtected comes with a 409 when a database delete is
	// refused because deletion protection is enabled.
	CodeDeletionProtected = "deletion_protected"
	// CodeDatabasesUnavailable comes with a 404 when managed databases are
	// not enabled on the platform. Reads, deletes and a PATCH that only
	// changes deletionProtection still work then, so existing databases
	// can be removed.
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
