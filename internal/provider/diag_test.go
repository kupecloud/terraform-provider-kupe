package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func TestAPIErrorDetail(t *testing.T) {
	// Sentinels picked deliberately:
	//   - roleHint is the substring uniquely produced by the 403/RBAC
	//     branch; presence/absence asserts whether we tagged on a
	//     credentials-focused hint.
	//   - schemaHint was the old 400 wording that mislead users hitting
	//     admission-webhook denials. After the rework the 400 branch
	//     emits only the verbatim API message, so any 400 case must
	//     NOT contain this sentinel.
	const (
		roleHint   = "lack the role required"
		schemaHint = "Check the schema"
	)

	tests := []struct {
		name         string
		err          error
		wantSubstr   []string // every entry must appear in the detail
		wantNoSubstr []string // none of these may appear
	}{
		{
			name:         "non-api errors pass through unchanged",
			err:          errors.New("network: connection refused"),
			wantSubstr:   []string{"network: connection refused"},
			wantNoSubstr: []string{"kupe API returned", roleHint},
		},
		{
			// Webhook denial that now arrives as 400. The base line must
			// surface the verbatim message; no schema hint, no role hint.
			name: "400 webhook denial shows verbatim message only",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    `unsupported Kubernetes version "1.32", supported versions: [1.35.5 1.34.8 1.33.12]`,
			},
			wantSubstr: []string{
				"kupe API returned 400",
				`unsupported Kubernetes version "1.32"`,
			},
			wantNoSubstr: []string{schemaHint, roleHint},
		},
		{
			// Kubernetes field causes arrive inside the 400 message and
			// must reach the user verbatim.
			name: "400 validation keeps field causes",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    `invalid database "orders-db": spec.resources.storage: Invalid value: "string": storage can only grow`,
			},
			wantSubstr: []string{"spec.resources.storage", "storage can only grow"},
		},
		{
			name: "404 databases_unavailable says the feature is off, not that the resource is gone",
			err: &client.APIError{
				StatusCode: http.StatusNotFound,
				Message:    "managed databases are not available on this platform",
				Code:       client.CodeDatabasesUnavailable,
			},
			wantSubstr:   []string{"Databases are not enabled for this Kupe Cloud environment", "their bindings deleted or have namespaces removed", "Error code: databases_unavailable"},
			wantNoSubstr: []string{"does not exist on the server"},
		},
		{
			name: "400 creation_paused says nothing was created and to retry later",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    "Database creation is temporarily paused while we expand platform capacity. Please try again later.",
				Code:       client.CodeCreationPaused,
			},
			wantSubstr:   []string{"Error code: creation_paused", "Creation is temporarily paused on the Kupe platform, so nothing was created", "Existing resources are not affected", "apply` again later"},
			wantNoSubstr: []string{"databases"},
		},
		{
			name: "400 storage_shrink_unsupported says storage only grows",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    "storage can only grow",
				Code:       client.CodeStorageShrinkUnsupported,
				Field:      "resources.storage",
			},
			wantSubstr: []string{"Error code: storage_shrink_unsupported", "Field: resources.storage", "storage can only grow. Keep the current size"},
		},
		{
			// True RBAC denial: bare "access denied" message from kupe-api.
			// The role hint is the actionable signal — keep it.
			name: "403 access denied shows role hint",
			err: &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    "access denied",
			},
			wantSubstr:   []string{"kupe API returned 403", roleHint},
			wantNoSubstr: nil,
		},
		{
			// Defensive: an older kupe-api that still routes webhook
			// denials through 403 returns a non-"access denied" message.
			// We must NOT show the role hint in that case — that's the
			// exact misleading behaviour this rework is fixing.
			name: "403 non-RBAC body suppresses role hint",
			err: &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    `unsupported Kubernetes version "1.32"`,
			},
			wantSubstr:   []string{"kupe API returned 403", `unsupported Kubernetes version`},
			wantNoSubstr: []string{roleHint},
		},
		{
			// Whitespace/case variations of the RBAC sentinel still count
			// as RBAC — the constant is normalised before comparison.
			name: "403 access denied with surrounding whitespace still tagged",
			err: &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    "  Access Denied  ",
			},
			wantSubstr: []string{roleHint},
		},
		{
			name: "401 carries credentials hint",
			err: &client.APIError{
				StatusCode: http.StatusUnauthorized,
				Message:    "invalid token",
			},
			wantSubstr: []string{"kupe API returned 401", "KUPE_API_KEY"},
		},
		{
			name: "404 carries refresh hint",
			err: &client.APIError{
				StatusCode: http.StatusNotFound,
				Message:    `cluster "smoke" not found`,
			},
			wantSubstr: []string{"kupe API returned 404", "terraform refresh"},
		},
		{
			name:       "plain 409 keeps the name-or-concurrent-change hint",
			err:        &client.APIError{StatusCode: http.StatusConflict, Message: `database "orders-db" already exists`},
			wantSubstr: []string{"a resource with this name already exists"},
		},
		{
			name: "409 terminating says it is being deleted, not that the name is taken",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeTerminating,
				Message: `database "orders-db" is being deleted`,
			},
			wantSubstr:   []string{"Error code: terminating", "It is being deleted, so it can no longer be changed or bound."},
			wantNoSubstr: []string{"already exists"},
		},
		{
			name: "409 tenant_terminating",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeTenantTerminating,
				Message: "tenant acme is being deleted; cannot create new databases",
			},
			wantSubstr:   []string{"Your tenant is being deleted, so nothing new can be created in it and nothing can be made larger."},
			wantNoSubstr: []string{"already exists", "clusters or databases"},
		},
		{
			name: "409 tenant_suspended points at support",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeTenantSuspended,
				Message: "tenant acme is suspended; contact support before growing databases",
			},
			wantSubstr:   []string{"Your tenant is suspended, so nothing new can be created", "Contact Kupe Cloud support to lift the suspension."},
			wantNoSubstr: []string{"already exists", "clusters or databases"},
		},
		{
			name: "409 payment_setup_required points at the Billing page",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodePaymentSetupRequired,
				Message: "tenant acme is awaiting payment setup; add a payment method before creating databases",
			},
			wantSubstr:   []string{"Add a payment method on the Billing page of the Kupe Console"},
			wantNoSubstr: []string{"already exists"},
		},
		{
			name:         "409 with a code this provider does not know shows the API's words only",
			err:          &client.APIError{StatusCode: http.StatusConflict, Code: "something_new", Message: "refused"},
			wantSubstr:   []string{"kupe API returned 409: refused", "Error code: something_new"},
			wantNoSubstr: []string{"already exists"},
		},
		{
			name: "400 plan_limit_reached says delete one or upgrade",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodePlanLimitReached,
				Message: "maximum database limit reached: your plan allows 2 databases",
			},
			wantSubstr: []string{"Error code: plan_limit_reached", "Delete one you no longer need or upgrade your plan"},
		},
		{
			name: "400 DATABASE_NAME_TOO_LONG says to choose a shorter name",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeDatabaseNameTooLong, Field: "metadata.name",
				Message: `database name "` + strings.Repeat("d", 45) + `" too long: at most 44 characters for this tenant`,
			},
			wantSubstr: []string{
				"Error code: DATABASE_NAME_TOO_LONG", "Field: metadata.name",
				"too long for your tenant, so nothing was created", "Choose a shorter `name`",
			},
		},
		{
			name: "404 registries_unavailable says what still works and that state is kept",
			err: &client.APIError{
				StatusCode: http.StatusNotFound, Code: client.CodeRegistriesUnavailable,
				Message: "registries are not available on this platform",
			},
			wantSubstr: []string{
				"Registries are not enabled for this Kupe Cloud environment",
				"deletion protection can still be turned off", "Terraform state is kept unchanged.",
				"Error code: registries_unavailable",
			},
			wantNoSubstr: []string{"does not exist on the server", "Databases are not enabled"},
		},
		{
			name: "a registry route the API does not serve is not a missing resource",
			err: &client.RegistriesNotServedError{Err: &client.APIError{
				StatusCode: http.StatusNotFound, Message: "404 page not found\n",
			}},
			wantSubstr:   []string{"does not offer registries yet", "Terraform state is kept unchanged."},
			wantNoSubstr: []string{"does not exist on the server"},
		},
		{
			name: "409 REGISTRY_ALREADY_EXISTS gives the import command",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeRegistryAlreadyExists,
				Message: "this tenant already has a registry",
			},
			wantSubstr:   []string{"A tenant has one registry.", "`terraform import kupe_registry.this registry`"},
			wantNoSubstr: []string{"Usually means"},
		},
		{
			name: "409 REGISTRY_BINDING_EXISTS gives the import command",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeRegistryBindingExists,
				Message: `cluster "prod" is already connected to the registry`,
			},
			wantSubstr: []string{"`terraform import kupe_registry_binding.this registry/<cluster>`"},
		},
		{
			name: "409 REGISTRY_TOKEN_LIMIT_REACHED says to revoke one",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeRegistryTokenLimitReached,
				Message: "the registry already has 50 access tokens; revoke one first",
			},
			wantSubstr: []string{"the registry already has 50 access tokens", "Revoke one you no longer use"},
		},
		{
			name: "400 REGISTRY_STORAGE_LIMIT_ABOVE_PLAN says to lower or remove the limit",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeRegistryStorageLimitAbovePlan,
				Field: "spec.storageLimit", Message: "storage limit 80Gi is above the 50Gi your plan allows",
			},
			wantSubstr: []string{"Field: spec.storageLimit", "Set a lower `storage_limit`"},
		},
		{
			name: "400 REGISTRY_PUSH_NAMESPACES_SERVICE_LIMIT explains it is apply-time",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeRegistryPushNamespacesServiceLimit,
				Message: "the registry's cluster connections can give at most 20 namespaces a push credential",
			},
			wantSubstr: []string{"every cluster connection of the registry together", "the plan could not check it"},
		},
		{
			name: "400 REGISTRY_TOKEN_EXPIRY_INVALID points at expires_in_days",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeRegistryTokenExpiryInvalid,
				Message: "expiresInDays must be between 1 and 365",
			},
			wantSubstr: []string{"between 1 and 365", "`expires_in_days`"},
		},
		{
			name: "403 REGISTRY_TOKEN_PUSH_ADMIN_ONLY explains a token needs admin, not the generic role hint",
			err: &client.APIError{
				StatusCode: http.StatusForbidden, Code: client.CodeRegistryTokenPushAdminOnly,
				Message: "only admins can create push access tokens",
			},
			wantSubstr:   []string{"An access token can only be created by a member with the `admin` role"},
			wantNoSubstr: []string{roleHint, "access = \"pull\""},
		},
		{
			name: "400 backups_unavailable says to remove backup",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeBackupsUnavailable, Field: "spec.backup",
				Message: "backups are not available on this platform; remove spec.backup",
			},
			wantSubstr: []string{
				"Error code: backups_unavailable", "Field: spec.backup",
				"Remove `backup` from the configuration, then run `terraform apply` again.",
			},
		},
		{
			name: "400 backups_unavailable on a restore says to remove source",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeBackupsUnavailable, Field: "spec.source",
				Message: "backups are not available on this platform",
			},
			wantSubstr:   []string{"Field: spec.source", "cannot be restored", "Remove `source` from the configuration"},
			wantNoSubstr: []string{"Remove `backup`", "kupe_database_backup"},
		},
		{
			name: "400 backups_unavailable on a manual backup says no backup was taken",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeBackupsUnavailable,
				Message: "backups are not available on this platform",
			},
			wantSubstr:   []string{"no backup was taken", "Remove the `kupe_database_backup` resource"},
			wantNoSubstr: []string{"Remove `backup`", "Remove `source`"},
		},
		{
			name: "404 backups_unavailable says the platform has no manual backups, not that something is gone",
			err: &client.APIError{
				StatusCode: http.StatusNotFound, Code: client.CodeBackupsUnavailable,
				Message: "backups are not available on this platform",
			},
			wantSubstr:   []string{"does not offer manual backups or restores yet"},
			wantNoSubstr: []string{"does not exist on the server", "terraform import"},
		},
		{
			name: "400 restore_source_not_found",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeRestoreSourceNotFound, Field: "spec.source",
				Message: `backup "nightly" not found`,
			},
			wantSubstr: []string{"Error code: restore_source_not_found", "named in `source` does not exist"},
		},
		{
			name: "400 restore_point_outside_window points at the restore window",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeRestorePointOutsideWindow,
				Field: "spec.source.pointInTime", Message: "the point is outside the restore window",
			},
			wantSubstr: []string{"`source.point_in_time` is outside the restore window", "`recovery_window.earliest`"},
		},
		{
			name: "400 restore_version_mismatch",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeRestoreVersionMismatch, Field: "spec.version",
				Message: "the version differs from the source's",
			},
			wantSubstr: []string{"runs its source's major version", "Remove `version`"},
		},
		{
			name: "400 restore_storage_too_small names source_storage",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest, Code: client.CodeRestoreStorageTooSmall,
				Field: "spec.resources.storage", Message: "storage is smaller than the source's",
			},
			wantSubstr: []string{"at least the storage its source had", "`source_storage`"},
		},
		{
			name: "409 restore_source_not_ready says to wait",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeRestoreSourceNotReady,
				Message: "the backup has not completed",
			},
			wantSubstr:   []string{"has not completed", "Wait for it"},
			wantNoSubstr: []string{"already exists on the tenant"},
		},
		{
			name: "409 backup_limit_reached says to delete one",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeBackupLimitReached,
				Message: "the database already has 20 manual backups",
			},
			wantSubstr:   []string{"keeps 20 manual backups", "Delete one you no longer need"},
			wantNoSubstr: []string{"already exists on the tenant"},
		},
		{
			name: "409 database_not_ready says to wait for Running",
			err: &client.APIError{
				StatusCode: http.StatusConflict, Code: client.CodeDatabaseNotReady,
				Message: "the database is not running",
			},
			wantSubstr:   []string{"no backup was taken", "phase is Running"},
			wantNoSubstr: []string{"already exists on the tenant"},
		},
		{
			name: "503 while the tenant's limit is being set up gets the retry hint",
			err: &client.APIError{
				StatusCode: http.StatusServiceUnavailable,
				Message:    "the tenant's database limit is still being set up; retry in a few seconds",
			},
			wantSubstr: []string{"still being set up", "Retry the operation after a short delay"},
		},
		{
			name: "wrapped api error is unwrapped",
			err: fmt.Errorf("client: %w", &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    "access denied",
			}),
			wantSubstr: []string{"kupe API returned 403", roleHint},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := apiErrorDetail(tt.err)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("missing substring %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.wantNoSubstr {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected substring %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

func TestDeleteErrorDetail(t *testing.T) {
	const (
		wrapper        = "delete not accepted before the timeout"
		deleteHint     = "kept refusing the delete"
		createConflict = "a resource with this name already exists"
	)
	conflict := fakeAPIError(http.StatusConflict, "the database changed while it was being deleted, retry")
	tests := []struct {
		name         string
		err          error
		wantSubstr   []string
		wantNoSubstr []string
	}{
		{
			name:         "a conflict that outlasted the timeout keeps the wrapper and gets the delete hint",
			err:          fmt.Errorf("%w: %w", errDeleteNotAccepted, conflict),
			wantSubstr:   []string{wrapper, "kupe API returned 409", deleteHint, "Run the same `terraform` command again"},
			wantNoSubstr: []string{createConflict},
		},
		{
			name:         "another error that outlasted the timeout keeps the wrapper and its own hint",
			err:          fmt.Errorf("%w: %w", errDeleteNotAccepted, fakeAPIError(http.StatusServiceUnavailable, "unavailable")),
			wantSubstr:   []string{wrapper, "kupe API returned 503", "temporarily unavailable"},
			wantNoSubstr: []string{deleteHint},
		},
		{
			name:         "a non-API error that outlasted the timeout is not prefixed twice",
			err:          fmt.Errorf("%w: %w", errDeleteNotAccepted, errors.New("connection refused")),
			wantSubstr:   []string{wrapper + ": connection refused"},
			wantNoSubstr: []string{wrapper + ": " + wrapper},
		},
		{
			name:         "an error returned at once is described as usual",
			err:          fakeAPIError(http.StatusForbidden, "access denied"),
			wantSubstr:   []string{"kupe API returned 403", "lack the role required"},
			wantNoSubstr: []string{wrapper, deleteHint},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deleteErrorDetail(tt.err)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("missing substring %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.wantNoSubstr {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected substring %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

// TestDeleteUntilAccepted_KeepsRefusalWhenCutOff: the attempt in flight when
// the timeout fires fails with a context error; the error returned must
// still be the refusal that kept the delete from being accepted.
func TestDeleteUntilAccepted_KeepsRefusalWhenCutOff(t *testing.T) {
	shrinkPollInterval(t)
	conflict := fakeAPIError(http.StatusConflict, "the database changed while it was being deleted, retry")
	calls := 0
	err := deleteUntilAccepted(context.Background(), 50*time.Millisecond, func(c context.Context) error {
		calls++
		if calls < 3 {
			return conflict
		}
		<-c.Done()
		return fmt.Errorf("executing request: %w", c.Err())
	})
	if !errors.Is(err, errDeleteNotAccepted) || !errors.Is(err, conflict) {
		t.Fatalf("expected the conflict wrapped as not accepted, got %v", err)
	}
}

// fakeAPIError returns a client.APIError shaped to match what the real
// client surfaces from a server response with the given status code.
func fakeAPIError(status int, msg string) error {
	return &client.APIError{StatusCode: status, Message: msg}
}

// TestIsTerminalAPIError pins which statuses a readiness/deletion poll
// treats as permanent (401/403/400 — surface immediately) versus transient
// (keep polling). Regression guard for LOW-1: before the fix every GET
// error inside the poll closures was swallowed, so a revoked key stalled to
// the full timeout and hid the real 401/403 behind a generic warning.
func TestIsTerminalAPIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not terminal", nil, false},
		{"400 is terminal", fakeAPIError(http.StatusBadRequest, "bad request"), true},
		{"401 is terminal", fakeAPIError(http.StatusUnauthorized, "invalid token"), true},
		{"403 is terminal", fakeAPIError(http.StatusForbidden, "access denied"), true},
		{"404 is not terminal", fakeAPIError(http.StatusNotFound, "not found"), false},
		{
			"404 databases_unavailable is terminal",
			&client.APIError{StatusCode: http.StatusNotFound, Code: client.CodeDatabasesUnavailable},
			true,
		},
		{
			"404 registries_unavailable is terminal",
			&client.APIError{StatusCode: http.StatusNotFound, Code: client.CodeRegistriesUnavailable},
			true,
		},
		{
			"404 backups_unavailable is terminal",
			&client.APIError{StatusCode: http.StatusNotFound, Code: client.CodeBackupsUnavailable},
			true,
		},
		{
			"a registry route the API does not serve is terminal",
			&client.RegistriesNotServedError{Err: &client.APIError{StatusCode: http.StatusNotFound, Message: "404 page not found"}},
			true,
		},
		{"409 is not terminal", fakeAPIError(http.StatusConflict, "already exists"), false},
		{"500 is not terminal", fakeAPIError(http.StatusInternalServerError, "boom"), false},
		{"wrapped 403 is terminal", fmt.Errorf("get: %w", fakeAPIError(http.StatusForbidden, "access denied")), true},
		{"non-api error is not terminal", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTerminalAPIError(tt.err); got != tt.want {
				t.Errorf("isTerminalAPIError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// shrinkPollInterval lowers the helper's poll interval so the tests run
// in millisecond-scale rather than seconds. Tests restore the original
// in a t.Cleanup.
func shrinkPollInterval(t *testing.T) {
	t.Helper()
	prev := defaultPollInterval
	defaultPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { defaultPollInterval = prev })
}

func TestWaitForCondition_DoneOnFirstCheck(t *testing.T) {
	// Resource is already in the desired state by the time we poll —
	// the function must return immediately without waiting an entire
	// poll interval. Mirrors the "delete that already completed by
	// the time apply hits the GET" case.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected nil on immediate done, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("expected immediate return, took %v", elapsed)
	}
}

func TestWaitForCondition_DoneAfterRetries(t *testing.T) {
	// Resource reaches the target state after several polls. Verifies
	// the loop converges rather than the exact timing.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	calls := 0
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		calls++
		return calls >= 3, nil
	})
	if err != nil {
		t.Fatalf("expected nil after convergence, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 calls, got %d", calls)
	}
}

func TestWaitForCondition_TimeoutSurfacesContextError(t *testing.T) {
	// Predicate never becomes true — context deadline bounds the wait
	// and the helper returns a wrapped DeadlineExceeded so callers can
	// map it to a user-facing warning.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected error to wrap context.DeadlineExceeded, got %v", err)
	}
}

func TestWaitForCondition_TerminalErrorSurfaces(t *testing.T) {
	// done=true with a non-nil err signals a terminal failure (a
	// future "phase=Degraded with reason" path could use this). The
	// helper must surface that error rather than returning nil.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	terminalErr := errors.New("resource entered terminal failure state")
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		return true, terminalErr
	})
	if !errors.Is(err, terminalErr) {
		t.Fatalf("expected the terminal error to surface, got %v", err)
	}
}

func TestWaitForCondition_DeletePredicate(t *testing.T) {
	// Realistic delete-flavoured usage: closure calls a GET and
	// reports done=true when the API returns 404. Mirrors what the
	// cluster/secret Delete handlers do internally.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	calls := 0
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		calls++
		// First two polls see the resource still terminating; third
		// sees the 404. The closure swallows the non-NotFound err
		// (transient) and returns done=false.
		if calls < 3 {
			return false, nil
		}
		if !client.IsNotFound(fakeAPIError(http.StatusNotFound, "not found")) {
			t.Fatal("test setup: IsNotFound predicate broken")
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected nil after the resource disappeared, got %v", err)
	}
}

func TestDeleteUntilAccepted(t *testing.T) {
	conflict := fakeAPIError(http.StatusConflict, "the object has been modified")
	protected := &client.APIError{StatusCode: http.StatusConflict, Code: client.CodeDeletionProtected}
	terminating := &client.APIError{StatusCode: http.StatusConflict, Code: client.CodeTerminating}
	tests := []struct {
		name      string
		responses []error // one per attempt; the last repeats
		wantErr   error   // matched with errors.Is; nil means success
		wantCalls int
	}{
		{name: "accepted at once", responses: []error{nil}, wantCalls: 1},
		{name: "already gone", responses: []error{fakeAPIError(http.StatusNotFound, "not found")}, wantCalls: 1},
		{name: "conflicts are retried", responses: []error{conflict, conflict, nil}, wantCalls: 3},
		{name: "deletion protection is returned at once", responses: []error{protected}, wantErr: protected, wantCalls: 1},
		{name: "any coded refusal is returned at once", responses: []error{terminating}, wantErr: terminating, wantCalls: 1},
		{name: "a persistent conflict returns the last refusal", responses: []error{conflict}, wantErr: conflict},
		{name: "a persistent conflict is marked not accepted", responses: []error{conflict}, wantErr: errDeleteNotAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shrinkPollInterval(t)
			calls := 0
			err := deleteUntilAccepted(context.Background(), 50*time.Millisecond, func(context.Context) error {
				resp := tt.responses[min(calls, len(tt.responses)-1)]
				calls++
				return resp
			})
			if tt.wantErr == nil && err != nil {
				t.Fatalf("expected success, got %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
			if tt.wantCalls != 0 && calls != tt.wantCalls {
				t.Errorf("delete called %d times, want %d", calls, tt.wantCalls)
			}
		})
	}
}

// TestRegistryChildCreateErrorDetail (F-2): only kupe-api's code-less 404 for
// a missing registry gets the hint to create the registry first; every
// other error keeps apiErrorDetail.
func TestRegistryChildCreateErrorDetail(t *testing.T) {
	notFound := func(code, msg string) *client.APIError {
		return &client.APIError{StatusCode: http.StatusNotFound, Code: code, Message: msg}
	}
	tests := []struct {
		name     string
		err      error
		wantHint bool
	}{
		{name: "missing registry", err: notFound("", `registry "registry" not found`), wantHint: true},
		{name: "wrapped missing registry", err: fmt.Errorf("create: %w", notFound("", `registry "registry" not found`)), wantHint: true},
		{name: "missing cluster connection", err: notFound("", `cluster connection "prod" not found`)},
		{name: "missing cluster", err: notFound("", `cluster "prod" not found`)},
		{name: "coded 404 naming the registry", err: notFound("registry_missing", `registry "registry" not found`)},
		{name: "registries unavailable", err: notFound(client.CodeRegistriesUnavailable, "registries are not available on this platform")},
		{name: "400 naming the registry", err: &client.APIError{StatusCode: http.StatusBadRequest, Message: `registry "registry" not found`}},
		{name: "not an API error", err: errors.New(`registry "registry" not found`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registryChildCreateErrorDetail(tt.err)
			if has := strings.Contains(got, registryMissingHint); has != tt.wantHint {
				t.Fatalf("detail = %q, want the no-registry hint: %v", got, tt.wantHint)
			}
			if !tt.wantHint && got != apiErrorDetail(tt.err) {
				t.Errorf("detail = %q, want apiErrorDetail %q", got, apiErrorDetail(tt.err))
			}
			if tt.wantHint && strings.Contains(got, "terraform refresh") {
				t.Errorf("detail = %q still has the generic 404 hint", got)
			}
		})
	}
}
