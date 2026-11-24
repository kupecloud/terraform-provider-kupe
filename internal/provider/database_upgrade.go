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

// Ways the platform holds a database on its old major after a raise.
const (
	holdRolledBack  = "rolled back"
	holdFailed      = "failed"
	holdSwitchedOff = "switched off"
)

// upgradeHold says how the platform is holding db on its old major, "" when
// it is not. A hold is only a verdict once the platform has seen the spec
// (observedGeneration caught up) and the spec still asks for a major above
// the one running; a reason left over from an earlier attempt is not one on a
// new change. Ready False with UpgradeRolledBack or UpgradeFailed is a
// rollback or failure. Ready False UpgradeSwitchedOff, or, from an operator
// that has no such reason, VersionUnsupported True MajorChangeUnsupported, is
// the platform no longer offering major upgrades. The older signal is stale
// while an upgrade runs, so it counts only when the phase is not Upgrading and
// Ready is not False with the reason of an upgrade that waits, takes its
// backup or runs.
func upgradeHold(db *client.Database) string {
	if db == nil || db.Status == nil || db.Status.ObservedGeneration < db.Generation || !upgradeRaised(db) {
		return ""
	}
	ready := findReady(db)
	if ready.Status == "False" {
		switch ready.Reason {
		case client.ReasonUpgradeRolledBack:
			return holdRolledBack
		case client.ReasonUpgradeFailed:
			return holdFailed
		case client.ReasonUpgradeSwitchedOff:
			return holdSwitchedOff
		case client.ReasonUpgradeWaiting, client.ReasonPreUpgradeBackup, client.ReasonUpgrading:
			return ""
		}
	}
	if db.Status.Phase == databasePhaseUpgrading {
		return ""
	}
	unsupported := conditionOf(db, client.ConditionVersionUnsupported)
	if unsupported.Status == "True" && unsupported.Reason == client.ReasonMajorChangeUnsupported {
		return holdSwitchedOff
	}
	return ""
}

// upgradeRaised reports whether db's spec asks for a major above the one it
// runs. False when either is unknown.
func upgradeRaised(db *client.Database) bool {
	want, okWant := majorOf(db.Version)
	running, okRunning := majorOf(db.Status.CurrentVersion)
	return okWant && okRunning && want > running
}

// conditionOf is db's condition of type t, the zero Condition when absent.
func conditionOf(db *client.Database, t string) client.Condition {
	if db != nil && db.Status != nil {
		for _, c := range db.Status.Conditions {
			if c.Type == t {
				return c
			}
		}
	}
	return client.Condition{}
}

func findReady(db *client.Database) client.Condition {
	return conditionOf(db, client.ConditionReady)
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
// holds the database on its old major, and explains it. Both are "" when it
// does not.
func databaseUpgradeFailure(db *client.Database) (summary, detail string) {
	hold := upgradeHold(db)
	if hold == "" {
		return "", ""
	}
	if hold == holdSwitchedOff {
		return "database upgrade is on hold", databaseUpgradeDetail(db)
	}
	return "database upgrade did not complete", databaseUpgradeDetail(db)
}

// holdMessage is what the platform says about the hold on db: Ready's
// message, or the VersionUnsupported message from an operator that has no
// switched-off reason.
func holdMessage(db *client.Database) string {
	if msg := findReady(db).Message; msg != "" && findReady(db).Reason == client.ReasonUpgradeSwitchedOff {
		return msg
	}
	if upgradeHold(db) == holdSwitchedOff {
		if msg := conditionOf(db, client.ConditionVersionUnsupported).Message; msg != "" {
			return msg
		}
	}
	return databaseReadyMessage(db)
}

// databaseUpgradeDetail explains a database held on its old major.
func databaseUpgradeDetail(db *client.Database) string {
	hold := upgradeHold(db)
	var detail string
	if hold == holdSwitchedOff {
		detail = fmt.Sprintf("The platform stopped offering major upgrades after major version %s was set for "+
			"database %q, so it holds the database on %s and does not start the upgrade.",
			db.Version, db.Name, db.Status.CurrentVersion)
	} else {
		detail = fmt.Sprintf("The upgrade of database %q to major version %s %s. It reports version %s.",
			db.Name, db.Version, hold, db.Status.CurrentVersion)
	}
	if msg := holdMessage(db); msg != "" {
		detail += "\n\nStatus message: " + msg
	}
	detail += "\n\nThe Kupe platform holds the database, with no platform updates, until you set `version` back " +
		"to the major it runs. " + cancelUpgradeHint(db)
	if hold == holdSwitchedOff {
		detail += " Raise `version` again once the platform offers major upgrades."
	} else {
		detail += " To try again, fix the cause and then raise `version` again."
	}
	detail += "\n\nThe change is recorded in Terraform state, so until you set `version` back " +
		"`terraform plan` shows no change even though the database runs its old major."
	return detail
}

// databaseUpgradeNotice is the warning a refresh gives for a database whose
// upgrade is held or still running, "" when there is nothing to say.
func databaseUpgradeNotice(db *client.Database) (summary, detail string) {
	switch {
	case upgradeHold(db) != "":
		return "database upgrade is on hold", databaseUpgradeDetail(db)
	case upgradeRunning(db):
		reason := client.ConditionReason(db.Status.Conditions, client.ConditionUpgradeInProgress)
		detail := fmt.Sprintf("Database %q has a major upgrade in progress (%s).", db.Name, reason)
		if reason == client.ReasonPreUpgradeBackup || reason == client.ReasonUpgrading {
			detail += " Changes to resources and high availability are refused until it finishes."
		}
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

// cancelsUpgrade reports whether setting db's version to planned goes back
// to the major it runs from a spec above it: a cancel, which the platform
// admits only in some states.
func cancelsUpgrade(planned string, db *client.Database) bool {
	if db == nil || db.Status == nil {
		return false
	}
	want, okWant := majorOf(planned)
	spec, okSpec := majorOf(db.Version)
	running, okRunning := majorOf(db.Status.CurrentVersion)
	return okWant && okSpec && okRunning && want == running && want < spec
}

// upgradeCancellable mirrors the operator webhook's rule for a cancel
// (kupe-control-operator#551): not while the database is offline for the
// upgrade; always when the UpgradeInProgress condition is missing or has not
// been written for this spec; otherwise only while it is True UpgradeWaiting
// or PreUpgradeBackup, or False UpgradeRolledBack, UpgradeFailed or
// UpgradeSwitchedOff. It keys on that condition, not on Ready, which a
// blocking check rewrites.
func upgradeCancellable(db *client.Database) bool {
	if db.Status.Phase == databasePhaseUpgrading {
		return false
	}
	c := findCondition(db, client.ConditionUpgradeInProgress)
	if c == nil || c.ObservedGeneration < db.Generation {
		return true
	}
	switch c.Status {
	case "True":
		return c.Reason == client.ReasonUpgradeWaiting || c.Reason == client.ReasonPreUpgradeBackup
	case "False":
		return c.Reason == client.ReasonUpgradeRolledBack || c.Reason == client.ReasonUpgradeFailed ||
			c.Reason == client.ReasonUpgradeSwitchedOff
	}
	return false
}

// upgradeSucceeded reports whether UpgradeInProgress is False
// UpgradeSucceeded as written for this spec: the upgrade has finished, though
// currentVersion may still show the old major.
func upgradeSucceeded(db *client.Database) bool {
	c := findCondition(db, client.ConditionUpgradeInProgress)
	return c != nil && c.Status == "False" && c.Reason == client.ReasonUpgradeSucceeded &&
		c.ObservedGeneration >= db.Generation
}

// findCondition is db's condition of type t, nil when absent.
func findCondition(db *client.Database, t string) *client.Condition {
	for i := range db.Status.Conditions {
		if db.Status.Conditions[i].Type == t {
			return &db.Status.Conditions[i]
		}
	}
	return nil
}

// cancelRefusal is why setting db back to the major it runs would not be a
// cancel, or "" when it is one (or when the platform should decide). An
// operator before the cancel fix reads a version equal to the running major
// as a cancel right after a successful upgrade, before currentVersion has
// moved, and would undo the upgrade; so a cancel is only sent when the
// platform would see something to cancel (upgradeCancellable), when Ready
// says so (rolled back, failed or switched off), or for the switched-off hold
// an older operator reports as VersionUnsupported. A finished upgrade is
// refused unless one of the holds applies.
func cancelRefusal(db *client.Database) (summary, detail string) {
	st := db.Status
	if st == nil || st.ObservedGeneration < db.Generation {
		return "database has not applied its last change yet",
			fmt.Sprintf("Database %q has not applied its last change yet, so setting `version` back could be "+
				"misread and nothing was sent. Try again in a moment: run `terraform plan`, then `terraform apply`.", db.Name)
	}
	if st.Phase == databasePhaseUpgrading {
		// Too late to cancel: the platform's own answer says so.
		return "", ""
	}
	ready := findReady(db)
	switch {
	case upgradeHold(db) == holdSwitchedOff:
		return "", ""
	case ready.Status == "False" && (ready.Reason == client.ReasonUpgradeRolledBack ||
		ready.Reason == client.ReasonUpgradeFailed || ready.Reason == client.ReasonUpgradeSwitchedOff):
		return "", ""
	case upgradeSucceeded(db):
		return "database upgrade has finished",
			fmt.Sprintf("The upgrade of database %q has finished, so there is nothing to cancel and nothing was "+
				"sent. Setting `version` to %s now would not undo it. Set `version` to %s in the configuration.",
				db.Name, runningMajor(db), db.Version)
	case upgradeCancellable(db):
		return "", ""
	}
	return "nothing to cancel",
		fmt.Sprintf("Database %q has no upgrade that is waiting, taking its backup, rolled back or failed, so "+
			"there is nothing to cancel and nothing was sent. Set `version` to %s in the configuration.", db.Name, db.Version)
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
