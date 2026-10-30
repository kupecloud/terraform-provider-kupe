package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// rbacForbiddenMessage is the verbatim message kupe-api returns for true
// RBAC denials (role lacks permission for this operation). Admission
// webhook denials surface via a different status code (400) and never
// match this string, so we use it as a discriminator: if a 403 carries
// this exact message we add the role-hint, otherwise we don't, so the
// user is not misled into thinking their API key is wrong when the API
// is really telling them e.g. "unsupported Kubernetes version".
const rbacForbiddenMessage = "access denied"

// defaultPollInterval is how often waitForCondition re-checks the server.
// 2s gives reasonable feedback latency without hammering kupe-api during
// a multi-minute teardown or provisioning operation. Tunable as a var so
// tests can shrink it without poking the helper itself.
var defaultPollInterval = 2 * time.Second

// waitForCondition polls fn at defaultPollInterval until it reports the
// terminal state (done=true) or the context expires.
//
//   - fn returns (true, nil): success — caller exits with nil
//   - fn returns (true, err): terminal failure observed — caller surfaces
//     err (pollUntil uses this for permanent API errors)
//   - fn returns (false, _): keep polling. Any transient error inside the
//     closure is the caller's responsibility to swallow; the helper
//     doesn't inspect it
//
// On timeout the helper returns a wrapped context error so callers can
// `errors.Is(err, context.DeadlineExceeded)` and decide whether to fail
// or warn. The caller passes a context with whatever timeout is
// appropriate for the resource type (cluster create: 15m via the
// user-overridable timeouts attribute; secret create: 2m; etc.).
//
// fn is also responsible for capturing observed state — typically by
// writing into the framework's `resp.State` from the surrounding
// handler — so an interrupted apply leaves Terraform with the latest
// values, not stale ones.
//
// Used by Create/Update (predicate: phase reached the Ready value) and
// Delete (predicate: GET returns IsNotFound).
func waitForCondition(ctx context.Context, fn func(context.Context) (bool, error)) error {
	// First check immediately; many fast operations complete before the
	// first ticker fires (creates that go straight to Running on a
	// warm cluster, deletes with no synced state to clean up).
	if done, err := fn(ctx); done {
		return err
	}

	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for resource to converge: %w", ctx.Err())
		case <-ticker.C:
			if done, err := fn(ctx); done {
				return err
			}
		}
	}
}

// pollUntil polls observe under timeout until it reports done. An error from
// observe ends the poll when isTerminalAPIError classifies it as permanent
// (a revoked key or lost role never clears by waiting); any other error is
// treated as transient and the poll continues, so a persistent fault ends as
// a timeout. observe ends the poll with its own error by returning done=true
// with it. observe is also where callers persist what they observed, so an
// interrupted apply keeps the latest values.
func pollUntil(ctx context.Context, timeout time.Duration, observe func(context.Context) (bool, error)) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return waitForCondition(waitCtx, func(c context.Context) (bool, error) {
		done, err := observe(c)
		if done || err == nil {
			return done, err
		}
		if isTerminalAPIError(err) {
			return true, err
		}
		return false, nil //nolint:nilerr // transient errors keep the poll going
	})
}

// waitForDeletion polls get until it returns 404. kupe-api accepts a DELETE
// as soon as the platform starts tearing the resource down; until the
// teardown finishes the name stays reserved, so an immediate re-create would
// conflict. Returns nil once gone, a terminal API error, or a timeout error.
func waitForDeletion(ctx context.Context, timeout time.Duration, get func(context.Context) error) error {
	return pollUntil(ctx, timeout, func(c context.Context) (bool, error) {
		err := get(c)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// errDeleteNotAccepted wraps the last refusal when deleteUntilAccepted runs
// out of time.
var errDeleteNotAccepted = errors.New("delete not accepted before the timeout")

// deleteUntilAccepted sends del until the API accepts it or reports the
// resource already gone. A plain 409 conflict (the resource kept changing
// while the delete was being admitted) is retried until the timeout, after
// which the last refusal is returned wrapped in errDeleteNotAccepted. A 409
// with an error code (deletion protection, for one) is a decided refusal and,
// like permanent API errors, is returned at once.
func deleteUntilAccepted(ctx context.Context, timeout time.Duration, del func(context.Context) error) error {
	var lastErr error
	err := pollUntil(ctx, timeout, func(c context.Context) (bool, error) {
		err := del(c)
		// An attempt cut short by the deadline says nothing about why the
		// delete was refused: keep the refusal before it.
		if c.Err() == nil || lastErr == nil {
			lastErr = err
		}
		switch {
		case err == nil || client.IsNotFound(err):
			return true, nil
		case client.ErrorCode(err) != "":
			return true, err
		}
		return false, err
	})
	if errors.Is(err, context.DeadlineExceeded) && lastErr != nil {
		return fmt.Errorf("%w: %w", errDeleteNotAccepted, lastErr)
	}
	return err
}

// deleteErrorDetail is apiErrorDetail for errors from deleteUntilAccepted. A
// delete that ran out of time keeps the "not accepted" context, and a 409
// gets a hint about the delete rather than about creating a resource.
func deleteErrorDetail(err error) string {
	var apiErr *client.APIError
	if !errors.Is(err, errDeleteNotAccepted) || !errors.As(err, &apiErr) {
		// apiErrorDetail keeps the whole message of a non-API error,
		// wrapper included.
		return apiErrorDetail(err)
	}
	if apiErr.StatusCode == http.StatusConflict {
		return errDeleteNotAccepted.Error() + ": " + apiErrorSummary(apiErr) +
			"\n\nThe Kupe platform kept refusing the delete because the resource was changing while the delete " +
			"was being admitted, so nothing was deleted. Run the same `terraform` command again; if it keeps failing, " +
			"contact Kupe Cloud support."
	}
	return errDeleteNotAccepted.Error() + ": " + apiErrorDetail(apiErr)
}

// registriesUnavailableHint explains the 404 registries_unavailable: what
// still works while the registry is switched off, and that state is kept.
const registriesUnavailableHint = "Registries are not enabled for this Kupe Cloud environment. An existing " +
	"registry, its cluster connections and its access tokens can still be read and deleted, deletion protection " +
	"can still be turned off and push namespaces removed; other changes are refused. Terraform state is kept " +
	"unchanged."

// backupsUnavailableHint explains the 400 backups_unavailable by the field
// it names: the backup settings (spec.backup), a restore (spec.source), or
// none for a manual backup, whose route is the request.
func backupsUnavailableHint(field string) string {
	switch field {
	case "spec.source":
		return "Backups are not available on this Kupe platform, so a database cannot be restored and nothing " +
			"was created. Remove `source` from the configuration, then run `terraform apply` again."
	case "":
		return "Backups are not available on this Kupe platform, so no backup was taken. Remove the " +
			"`kupe_database_backup` resource from the configuration, then run `terraform apply` again."
	default:
		return "Backups are not available on this Kupe platform. Remove `backup` from the configuration, then " +
			"run `terraform apply` again."
	}
}

// backupsNotOfferedHint explains the 404 backups_unavailable: this Kupe
// platform has no manual backups or restores at all.
const backupsNotOfferedHint = "This Kupe platform does not offer manual backups or restores yet, so nothing was " +
	"created. Remove the `kupe_database_backup` resource or `source` from the configuration, or contact Kupe " +
	"Cloud support."

// registriesNotServedHint explains a registry route the API does not have:
// the kupe API at this host predates registries.
const registriesNotServedHint = "The kupe API at this host does not offer registries yet, so nothing was " +
	"changed and Terraform state is kept unchanged. Check the provider's `host`, or contact Kupe Cloud support."

// registryMissingHint explains a cluster connection or access token create
// that found no registry: in one configuration, nothing ordered it after the
// registry's own create (F-2).
const registryMissingHint = "The tenant has no registry yet. Create it with `kupe_registry` first; in one " +
	"configuration, reference it (`registry = kupe_registry.this.name`) so Terraform creates it before this " +
	"resource."

// registryMissingMessage is kupe-api's code-less 404 for a registry that
// does not exist (`registry "registry" not found`).
var registryMissingMessage = regexp.MustCompile(`^registry "[^"]*" not found$`)

// registryChildCreateErrorDetail is apiErrorDetail for the create of a
// cluster connection or access token: a 404 saying the registry does not
// exist gets registryMissingHint in place of the generic 404 hint (F-2).
func registryChildCreateErrorDetail(err error) string {
	var apiErr *client.APIError
	if client.IsNotFound(err) && errors.As(err, &apiErr) && apiErr.Code == "" &&
		registryMissingMessage.MatchString(apiErr.Message) {
		return apiErrorSummary(apiErr) + "\n\n" + registryMissingHint
	}
	return apiErrorDetail(err)
}

// apiErrorSummary is the status, message and, when present, the canonical
// error code and field path of an API error. The code is a stable identifier
// users can quote to support or branch on.
func apiErrorSummary(apiErr *client.APIError) string {
	summary := fmt.Sprintf("kupe API returned %d: %s", apiErr.StatusCode, apiErr.Message)
	if apiErr.Code != "" {
		summary += "\n\nError code: " + apiErr.Code
		if apiErr.Field != "" {
			summary += "\nField: " + apiErr.Field
		}
	}
	return summary
}

// isTerminalAPIError reports whether err (or anything it wraps) is a
// *client.APIError carrying a status a readiness/deletion poll must treat
// as permanent rather than transient. 400 (the request is malformed or was
// rejected by an admission webhook), 401 (the credential was revoked or is
// invalid), and 403 (the credential lacks the required role) will never
// clear by waiting: continuing to poll only stalls to the full timeout and
// buries the real cause behind a generic "timed out" warning. A plain 404 is
// deliberately excluded — it is the success signal for deletion polls and
// for readiness polls means the object simply isn't observable yet, not a
// permanent failure. The 404s that say managed databases or the registry are
// not enabled are permanent and included, as is a registry route the API does
// not serve at all.
func isTerminalAPIError(err error) bool {
	if client.IsRegistriesNotServed(err) {
		return true
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return true
	case http.StatusNotFound:
		// A disabled feature does not come back by waiting.
		return apiErr.Code == client.CodeDatabasesUnavailable || apiErr.Code == client.CodeRegistriesUnavailable ||
			apiErr.Code == client.CodeBackupsUnavailable
	default:
		return false
	}
}

// apiErrorDetail returns a user-actionable detail string for a kupe-api
// error. The intent is that the user sees not just "kupe api: 401 …" in
// their diagnostic but also a one-sentence pointer at how to fix it. For
// any error that does not unwrap to *client.APIError (network failures,
// JSON decode errors, etc.) the original message is preserved unchanged.
//
// Used by all resources/datasources that call into the API client: the
// summary stays as the verb-driven "failed to {create,read,update,delete}
// {resource}" so diagnostics group sensibly, the detail explains what went
// wrong server-side and what the operator can do about it.
func apiErrorDetail(err error) string {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}

	base := apiErrorSummary(apiErr)
	if client.IsRegistriesNotServed(err) {
		return base + "\n\n" + registriesNotServedHint
	}
	switch apiErr.StatusCode {
	case http.StatusBadRequest:
		// Codes with a known remedy get one. creation_paused is shared by
		// clusters and databases, so its text names neither.
		switch apiErr.Code {
		case client.CodeCreationPaused:
			return base + "\n\nCreation is temporarily paused on the Kupe platform, so nothing was created. " +
				"Existing resources are not affected. Run `terraform apply` again later."
		case client.CodeStorageShrinkUnsupported:
			return base + "\n\nDatabase storage can only grow. Keep the current size or set a larger one."
		case client.CodePlanLimitReached:
			return base + "\n\nYour plan has no room for another one, so nothing was created. Delete one you no " +
				"longer need or upgrade your plan, then run `terraform apply` again."
		case client.CodeDatabaseNameTooLong:
			return base + "\n\nThe database name is too long for your tenant, so nothing was created. Database " +
				"names may be at most 44 characters, and fewer when the tenant name is over 12 characters. Choose a " +
				"shorter `name`, then run `terraform apply` again."
		case client.CodeRegistryStorageLimitAbovePlan:
			return base + "\n\nSet a lower `storage_limit`, or remove it to use the most your plan allows."
		case client.CodeRegistryPushNamespacesServiceLimit:
			return base + "\n\nThis limit counts the push namespaces of every cluster connection of the registry " +
				"together, so the plan could not check it. Remove push namespaces from this or another cluster " +
				"connection, then run `terraform apply` again."
		case client.CodeRegistryTokenExpiryInvalid:
			return base + "\n\nSet `expires_in_days` to a number of days the platform accepts, then run " +
				"`terraform apply` again."
		case client.CodeBackupsUnavailable:
			return base + "\n\n" + backupsUnavailableHint(apiErr.Field)
		case client.CodeRestoreSourceNotFound:
			return base + "\n\nThe backup or database named in `source` does not exist, so nothing was created. " +
				"Check the name, then run `terraform apply` again."
		case client.CodeRestorePointOutsideWindow:
			return base + "\n\n`source.point_in_time` is outside the restore window of the database named in " +
				"`source`, so nothing was created. Choose a point between its `recovery_window.earliest` and " +
				"`recovery_window.latest`, then run `terraform apply` again."
		case client.CodeRestoreVersionMismatch:
			return base + "\n\nA restored database runs its source's major version, so nothing was created. " +
				"Remove `version`, or set the source's version, then run `terraform apply` again."
		case client.CodeRestoreStorageTooSmall:
			return base + "\n\nA restored database needs at least the storage its source had, so nothing was " +
				"created. Set `resources.storage` to at least that size (a backup's `source_storage`), then run " +
				"`terraform apply` again."
		}
		// 400 covers two server-side outcomes that surface here:
		// (a) the request body failed schema decoding (typo in
		//     body_json / routes_json, missing required field), and
		// (b) an admission webhook on the platform rejected the
		//     request as semantically invalid (e.g. unsupported
		//     Kubernetes version, immutable field changed). In both
		//     cases the verbatim API message is the actionable signal;
		//     adding a schema-focused hint here would mislead users
		//     hitting case (b). Surface the message as-is.
		return base
	case http.StatusUnauthorized:
		return base + "\n\nThe kupe API rejected your credentials. Verify that KUPE_API_KEY (or KUPE_TOKEN) is set and that the value is current for this tenant — API keys can be revoked from the console."
	case http.StatusForbidden:
		// Only show the role hint for true RBAC denials. The kupe-api
		// returns "access denied" as the verbatim message for those;
		// any other 403 message we receive is from an older API that
		// still routed webhook denials through 403 (newer API uses
		// 400). For non-RBAC bodies the role hint is misleading.
		if apiErr.Code == client.CodeRegistryTokenPushAdminOnly {
			return base + "\n\nAn access token can only be created by a member with the `admin` role, or an " +
				"admin API key. Use admin credentials."
		}
		if strings.EqualFold(strings.TrimSpace(apiErr.Message), rbacForbiddenMessage) {
			return base + "\n\nYour credentials authenticated but lack the role required for this operation. Most mutating operations require the `admin` role on the tenant; reads accept `readonly` as well."
		}
		return base
	case http.StatusNotFound:
		if apiErr.Code == client.CodeDatabasesUnavailable {
			return base + "\n\nDatabases are not enabled for this Kupe Cloud environment. Existing databases can still be read, have deletion protection turned off, and be deleted, and their bindings deleted or have namespaces removed; other changes are refused. Terraform state is kept unchanged. Contact Kupe Cloud support if you expected databases to be available."
		}
		if apiErr.Code == client.CodeRegistriesUnavailable {
			return base + "\n\n" + registriesUnavailableHint
		}
		if apiErr.Code == client.CodeBackupsUnavailable {
			return base + "\n\n" + backupsNotOfferedHint
		}
		return base + "\n\nThe resource does not exist on the server. If you expected it to exist it may have been deleted out-of-band; run `terraform refresh` to reconcile or `terraform import` to re-attach."
	case http.StatusConflict:
		// A code names the refusal; only a plain conflict is about the name
		// or a concurrent change. The tenant refusals are shared by
		// clusters and databases, so their text names neither.
		switch apiErr.Code {
		case "":
			return base + "\n\nThe operation conflicts with current server state. Usually means a resource with this name already exists on the tenant or is being modified concurrently."
		case client.CodeTerminating:
			return base + "\n\nIt is being deleted, so it can no longer be changed or bound. Once it is gone, " +
				"`terraform apply` creates it again; remove it from the configuration if that is not wanted."
		case client.CodeTenantTerminating:
			return base + "\n\nYour tenant is being deleted, so nothing new can be created in it and nothing can " +
				"be made larger."
		case client.CodeTenantSuspended:
			return base + "\n\nYour tenant is suspended, so nothing new can be created in it and nothing can be " +
				"made larger. Contact Kupe Cloud support to lift the suspension."
		case client.CodeRegistryAlreadyExists:
			return base + "\n\nA tenant has one registry. Import it: `terraform import kupe_registry.this registry`."
		case client.CodeRegistryBindingExists:
			return base + "\n\nA cluster has one cluster connection. Import it: " +
				"`terraform import kupe_registry_binding.this registry/<cluster>`."
		case client.CodeRegistryTokenLimitReached:
			return base + "\n\nThe registry holds as many access tokens as it can. Revoke one you no longer use " +
				"(destroy its `kupe_registry_token`, or revoke it in the Kupe Console), then run `terraform apply` again."
		case client.CodeRestoreSourceNotReady:
			return base + "\n\nThe backup named in `source` has not completed, or the database named in `source` " +
				"has no restore window yet, so nothing was created. Wait for it, then run `terraform apply` again."
		case client.CodeBackupLimitReached:
			return base + "\n\nThe database already keeps 20 manual backups, the most it can, so no backup was " +
				"taken. Delete one you no longer need, then run `terraform apply` again."
		case client.CodeDatabaseNotReady:
			return base + "\n\nThe database is not running, so no backup was taken. Wait until its phase is " +
				"Running, then run `terraform apply` again."
		case client.CodePaymentSetupRequired:
			return base + "\n\nAdd a payment method on the Billing page of the Kupe Console, then run " +
				"`terraform apply` again."
		}
		return base
	case http.StatusPreconditionFailed:
		return base + "\n\nThe server-side state changed while this apply was running (ETag/If-Match mismatch). Another client modified the same resource concurrently — refresh and re-apply."
	case http.StatusTooManyRequests:
		return base + "\n\nRate limit exceeded. Retry the operation after a short delay; large batch applies may need `-parallelism=1` against the kupe API."
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return base + "\n\nThe Kupe Cloud API encountered an internal error or is temporarily unavailable. Retry the operation after a short delay; if the failure persists, check the status page or contact Kupe Cloud support."
	default:
		return base
	}
}
