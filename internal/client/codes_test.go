package client

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// codeWireValues ties every exported Code* constant to the literal value
// kupe-api sends in the error envelope's `code` field. The literals are
// copied from kupe-api internal/errors/errors.go, not built from the
// constants, so a typo in a constant's value fails here even though every
// test that builds an error from the same constant still passes.
var codeWireValues = []struct {
	name string
	got  string
	want string
}{
	{"CodeAccessTokenAlreadyCollected", CodeAccessTokenAlreadyCollected, "ACCESS_TOKEN_ALREADY_COLLECTED"},
	{"CodeAccessTokenCollectCreatorOnly", CodeAccessTokenCollectCreatorOnly, "ACCESS_TOKEN_COLLECT_CREATOR_ONLY"},
	{"CodeAccessTokenExpiryInvalid", CodeAccessTokenExpiryInvalid, "ACCESS_TOKEN_EXPIRY_INVALID"},
	{"CodeAccessTokenFailed", CodeAccessTokenFailed, "ACCESS_TOKEN_FAILED"},
	{"CodeAccessTokenLimitReached", CodeAccessTokenLimitReached, "ACCESS_TOKEN_LIMIT_REACHED"},
	{"CodeAccessTokenNotFound", CodeAccessTokenNotFound, "ACCESS_TOKEN_NOT_FOUND"},
	{"CodeAccessTokenNotReady", CodeAccessTokenNotReady, "ACCESS_TOKEN_NOT_READY"},
	{"CodeAlertmanagerEmailUnsupported", CodeAlertmanagerEmailUnsupported, "alertmanager_email_unsupported"},
	{"CodeAppsUnavailable", CodeAppsUnavailable, "apps_unavailable"},
	{"CodeBackupLimitReached", CodeBackupLimitReached, "backup_limit_reached"},
	{"CodeBackupsUnavailable", CodeBackupsUnavailable, "backups_unavailable"},
	{"CodeClusterNotFound", CodeClusterNotFound, "CLUSTER_NOT_FOUND"},
	{"CodeCreationPaused", CodeCreationPaused, "creation_paused"},
	{"CodeCredentialsNotDedicated", CodeCredentialsNotDedicated, "credentials_not_dedicated"},
	{"CodeDatabaseNameTooLong", CodeDatabaseNameTooLong, "DATABASE_NAME_TOO_LONG"},
	{"CodeDatabaseNotReady", CodeDatabaseNotReady, "database_not_ready"},
	{"CodeDatabasePoolerUnavailable", CodeDatabasePoolerUnavailable, "DATABASE_POOLER_UNAVAILABLE"},
	{"CodeDatabasesUnavailable", CodeDatabasesUnavailable, "databases_unavailable"},
	{"CodeDedicatedCredentialsIrreversible", CodeDedicatedCredentialsIrreversible, "dedicated_credentials_irreversible"},
	{"CodeDeletionProtected", CodeDeletionProtected, "deletion_protected"},
	{"CodeExtensionNotAllowed", CodeExtensionNotAllowed, "extension_not_allowed"},
	{"CodeImageNotFound", CodeImageNotFound, "image_not_found"},
	{"CodeImagePlatformUnsupported", CodeImagePlatformUnsupported, "image_platform_unsupported"},
	{"CodeImagePolicyDenied", CodeImagePolicyDenied, "image_policy_denied"},
	{"CodeImageUnavailable", CodeImageUnavailable, "image_unavailable"},
	{"CodeInvalidBindingCredentials", CodeInvalidBindingCredentials, "invalid_binding_credentials"},
	{"CodeMajorUpgradeUnavailable", CodeMajorUpgradeUnavailable, "major_upgrade_unavailable"},
	{"CodePaymentSetupRequired", CodePaymentSetupRequired, "payment_setup_required"},
	{"CodePlanLimitReached", CodePlanLimitReached, "plan_limit_reached"},
	{"CodeRegistriesUnavailable", CodeRegistriesUnavailable, "registries_unavailable"},
	{"CodeRegistryAlreadyExists", CodeRegistryAlreadyExists, "REGISTRY_ALREADY_EXISTS"},
	{"CodeRegistryBindingExists", CodeRegistryBindingExists, "REGISTRY_BINDING_EXISTS"},
	{"CodeRegistryPushNamespacesLimit", CodeRegistryPushNamespacesLimit, "REGISTRY_PUSH_NAMESPACES_LIMIT"},
	{"CodeRegistryPushNamespacesServiceLimit", CodeRegistryPushNamespacesServiceLimit, "REGISTRY_PUSH_NAMESPACES_SERVICE_LIMIT"},
	{"CodeRegistryStorageLimitAbovePlan", CodeRegistryStorageLimitAbovePlan, "REGISTRY_STORAGE_LIMIT_ABOVE_PLAN"},
	{"CodeRegistryTokenExpiryInvalid", CodeRegistryTokenExpiryInvalid, "REGISTRY_TOKEN_EXPIRY_INVALID"},
	{"CodeRegistryTokenLimitReached", CodeRegistryTokenLimitReached, "REGISTRY_TOKEN_LIMIT_REACHED"},
	{"CodeRegistryTokenPushAdminOnly", CodeRegistryTokenPushAdminOnly, "REGISTRY_TOKEN_PUSH_ADMIN_ONLY"},
	{"CodeRestorePointOutsideWindow", CodeRestorePointOutsideWindow, "restore_point_outside_window"},
	{"CodeRestoreSourceNotFound", CodeRestoreSourceNotFound, "restore_source_not_found"},
	{"CodeRestoreSourceNotReady", CodeRestoreSourceNotReady, "restore_source_not_ready"},
	{"CodeRestoreStorageTooSmall", CodeRestoreStorageTooSmall, "restore_storage_too_small"},
	{"CodeRestoreVersionMismatch", CodeRestoreVersionMismatch, "restore_version_mismatch"},
	{"CodeRoleInUse", CodeRoleInUse, "role_in_use"},
	{"CodeRoleNotDeclared", CodeRoleNotDeclared, "role_not_declared"},
	{"CodeRoleReserved", CodeRoleReserved, "role_reserved"},
	{"CodeSecretPathReserved", CodeSecretPathReserved, "secret_path_reserved"},
	{"CodeSecretVersionConflict", CodeSecretVersionConflict, "secret_version_conflict"},
	{"CodeSecretWriteUnconfirmed", CodeSecretWriteUnconfirmed, "secret_write_unconfirmed"},
	{"CodeStorageShrinkUnsupported", CodeStorageShrinkUnsupported, "storage_shrink_unsupported"},
	{"CodeTenantSuspended", CodeTenantSuspended, "tenant_suspended"},
	{"CodeTenantTerminating", CodeTenantTerminating, "tenant_terminating"},
	{"CodeTerminating", CodeTerminating, "terminating"},
	{"CodeVaultNotConfigured", CodeVaultNotConfigured, "vault_not_configured"},
	{"CodeVaultUnavailable", CodeVaultUnavailable, "vault_unavailable"},
}

// TestCodeWireValues_MatchKupeAPI fails when a constant's value drifts from
// the wire value kupe-api sends.
func TestCodeWireValues_MatchKupeAPI(t *testing.T) {
	for _, tt := range codeWireValues {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %q; kupe-api sends %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestCodeWireValues_CoverEveryConstant fails when a Code* constant is
// added to the package without a row in codeWireValues, or a row names a
// constant that no longer exists.
func TestCodeWireValues_CoverEveryConstant(t *testing.T) {
	declared := declaredCodeConstants(t)

	inTable := make(map[string]bool, len(codeWireValues))
	for _, tt := range codeWireValues {
		if inTable[tt.name] {
			t.Errorf("%s has more than one row", tt.name)
		}
		inTable[tt.name] = true
	}

	var missing, stale []string
	for name := range declared {
		if !inTable[name] {
			missing = append(missing, name)
		}
	}
	for name := range inTable {
		if !declared[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("constants without a wire-value row (copy the value from kupe-api internal/errors/errors.go): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("rows for constants that are not declared: %v", stale)
	}
}

// declaredCodeConstants returns the names of the exported Code* constants
// declared in the package's non-test Go files.
func declaredCodeConstants(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	names := map[string]bool{}
	for _, e := range entries {
		file := e.Name()
		if e.IsDir() || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				for _, ident := range spec.(*ast.ValueSpec).Names {
					if strings.HasPrefix(ident.Name, "Code") && ident.IsExported() {
						names[ident.Name] = true
					}
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("found no Code* constants; the package layout changed")
	}
	return names
}
