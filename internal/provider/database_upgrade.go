package provider

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// majorOf is the major of an engine version: "18" and "18.2" are 18. ok is
// false for "" and for anything that does not start with a number, which
// callers leave for the API to judge.
func majorOf(version string) (major int, ok bool) {
	head, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	major, err := strconv.Atoi(head)
	return major, err == nil && major >= 0
}

// upgradeAsked reports whether db's spec asks for a major other than the one
// it runs. It is false when either is unknown: a status with no
// currentVersion yet says nothing about an upgrade.
func upgradeAsked(db *client.Database) bool {
	if db == nil || db.Status == nil {
		return false
	}
	want, okWant := majorOf(db.Version)
	running, okRunning := majorOf(db.Status.CurrentVersion)
	return okWant && okRunning && want != running
}

// runsSpecMajor reports whether the database runs the major its spec asks
// for. Unknown versions count as yes: only a known difference keeps a
// database from converging.
func runsSpecMajor(db *client.Database) bool {
	return !upgradeAsked(db)
}

// upgradeHeld reports whether the platform is holding db on its old major
// after an upgrade rolled back or failed: Ready is False with one of those
// reasons, the platform has seen the spec, and the spec still asks for the
// new major. A reason left over from an earlier attempt, before
// observedGeneration catches up with a new change, is not a verdict on it.
func upgradeHeld(db *client.Database) bool {
	if db == nil || db.Status == nil || db.Status.ObservedGeneration < db.Generation || !upgradeAsked(db) {
		return false
	}
	switch client.ConditionReason(db.Status.Conditions, client.ConditionReady) {
	case client.ReasonUpgradeRolledBack, client.ReasonUpgradeFailed:
		return true
	}
	return false
}

// upgradeRunning reports whether the UpgradeInProgress condition is True:
// an upgrade is waiting, taking its backup or running.
func upgradeRunning(db *client.Database) bool {
	return db != nil && db.Status != nil &&
		client.ConditionTrue(db.Status.Conditions, client.ConditionUpgradeInProgress)
}

// runningMajor is the major the database runs, "" when it does not say.
func runningMajor(db *client.Database) string {
	if db == nil || db.Status == nil {
		return ""
	}
	if major, ok := majorOf(db.Status.CurrentVersion); ok {
		return strconv.Itoa(major)
	}
	return ""
}

// cancelUpgradeHint says how to back out of an upgrade the database is held
// in or still waiting for.
func cancelUpgradeHint(db *client.Database) string {
	major := runningMajor(db)
	if major == "" {
		return "To cancel, set `version` back to the major the database runs and run `terraform apply`."
	}
	return fmt.Sprintf("To cancel, set `version = %q` and run `terraform apply`.", major)
}

// databaseUpgradeFailure names an apply that ended because the platform
// rolled the upgrade back or it failed, and explains it. Both are "" when db
// is not held.
func databaseUpgradeFailure(db *client.Database) (summary, detail string) {
	if !upgradeHeld(db) {
		return "", ""
	}
	return "database upgrade did not complete", databaseUpgradeDetail(db)
}

// databaseUpgradeDetail explains a database held on its old major.
func databaseUpgradeDetail(db *client.Database) string {
	reason := client.ConditionReason(db.Status.Conditions, client.ConditionReady)
	outcome := "rolled back"
	if reason == client.ReasonUpgradeFailed {
		outcome = "failed"
	}
	detail := fmt.Sprintf("The upgrade of database %q to major version %s %s. It reports version %s.",
		db.Name, db.Version, outcome, db.Status.CurrentVersion)
	if msg := databaseReadyMessage(db); msg != "" {
		detail += "\n\nStatus message: " + msg
	}
	detail += "\n\nThe Kupe platform holds the database, with no platform updates, until you set `version` back " +
		"to the major it runs. " + cancelUpgradeHint(db) + " To try again, fix the cause and then raise " +
		"`version` again.\n\nThe change is recorded in Terraform state, so until you set `version` back " +
		"`terraform plan` shows no change even though the database runs its old major."
	return detail
}

// databaseUpgradeNotice is the warning a refresh gives for a database whose
// upgrade is held or still running, "" when there is nothing to say.
func databaseUpgradeNotice(db *client.Database) (summary, detail string) {
	switch {
	case upgradeHeld(db):
		return "database upgrade is on hold", databaseUpgradeDetail(db)
	case upgradeRunning(db):
		reason := client.ConditionReason(db.Status.Conditions, client.ConditionUpgradeInProgress)
		detail := fmt.Sprintf("Database %q has a major upgrade in progress (%s). Other changes are refused until it "+
			"finishes.", db.Name, reason)
		if msg := client.ConditionMessage(db.Status.Conditions, client.ConditionUpgradeInProgress); msg != "" {
			detail += "\n\nStatus message: " + msg
		}
		if reason != client.ReasonUpgrading {
			detail += "\n\n" + cancelUpgradeHint(db)
		}
		return "database upgrade in progress", detail
	}
	return "", ""
}

// databaseUpgradeTimedOutDetail adds to a timeout that fired on an upgrade
// still waiting, taking its backup or running.
func databaseUpgradeTimedOutDetail(db *client.Database) string {
	if !upgradeRunning(db) {
		return ""
	}
	reason := client.ConditionReason(db.Status.Conditions, client.ConditionUpgradeInProgress)
	detail := fmt.Sprintf("\n\nThe upgrade is at %s.", reason)
	switch reason {
	case client.ReasonUpgradeWaiting:
		detail += " It starts once the check in the status message is met. " + cancelUpgradeHint(db)
	case client.ReasonPreUpgradeBackup:
		detail += " The pre-upgrade backup comes first and takes longer for a large database. " + cancelUpgradeHint(db)
	case client.ReasonUpgrading:
		detail += " The database is offline until it finishes, and cannot be cancelled from here."
	}
	return detail
}

// databaseVersionOvertaken is the detail of an update whose planned version
// is no longer the database's: another client changed it (for example
// cancelled the upgrade) while this apply waited. "" when the planned
// version, or an unknown one, still holds.
func databaseVersionOvertaken(planned string, db *client.Database) string {
	if planned == "" || db == nil || planned == db.Version {
		return ""
	}
	return fmt.Sprintf("Database %q was changed outside this apply: its version is now %q, not the %q this apply "+
		"set, so the apply did not complete. Run `terraform plan` to review the current values, then apply "+
		"again.", db.Name, db.Version, planned)
}
