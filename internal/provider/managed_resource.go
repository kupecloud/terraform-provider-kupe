package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// managedResource is the lifecycle the managed services' resources share:
// Configure; Read, which drops a resource the API no longer has; Create and
// Update, which record what the API returned before waiting for the resource
// to be ready; an ETag update retried once when only status moved; and a
// Delete that is retried while the API refuses it for a concurrent change,
// then confirmed gone. A resource embeds it with its Terraform model M and
// API object A. It keeps its own schema, plan modifiers, requests and
// diagnostics: Create and Update build the request and hand the result here,
// and a resource can shadow any method.
type managedResource[M, A any] struct {
	client *client.Client
	kind   *serviceKind[M, A]
}

// serviceKind describes one managed service to managedResource.
type serviceKind[M, A any] struct {
	// noun names the service in diagnostics, e.g. "database".
	noun string
	// name is the name diagnostics give the resource in m.
	name func(m *M) string
	// get reads the resource m describes, with its ETag ("" when the API
	// sends none); del deletes it.
	get func(ctx context.Context, c *client.Client, m *M) (*A, string, error)
	del func(ctx context.Context, c *client.Client, m *M) error
	// mapToState records obj and its ETag in m.
	mapToState func(obj *A, etag string, m *M)
	// timeoutsOf is m's `timeouts` attribute; defaults apply where it is
	// unset.
	timeoutsOf func(m *M) timeouts.Value
	defaults   operationTimeouts
	ready      readiness[M, A]
	// settingsChanged lists, by attribute name, the settings an update can
	// change whose value in current differs from prior. nil: an update
	// refused for a stale ETag is not retried.
	settingsChanged func(prior *M, current *A) []string
	// deleteRefused, when set, recognises a refusal that ends Delete with
	// its own diagnostic instead of the generic one.
	deleteRefused func(m *M, err error) (summary, detail string, refused bool)
	// stillDeleting is the detail of the warning given when the delete
	// timeout fires before the resource is gone.
	stillDeleting func(m *M) string
}

// operationTimeouts are the defaults of the `timeouts` attribute.
type operationTimeouts struct {
	create, update, delete time.Duration
}

// of is the default for op ("create" or "update").
func (t operationTimeouts) of(op string) time.Duration {
	if op == "update" {
		return t.update
	}
	return t.create
}

// readiness says what Create and Update wait for.
type readiness[M, A any] struct {
	// target is the phase waited for, as diagnostics name it: "Running".
	target string
	// phase is obj's phase, "" when unknown.
	phase func(obj *A) string
	// converged reports whether obj has reached target with the latest
	// change applied.
	converged func(obj *A) bool
	// failed, when set, reports a state waiting is not expected to clear,
	// and how long it is ridden out before it ends the wait (0: at once).
	failed func(obj *A) (grace time.Duration, failed bool)
	// message, when set, is what obj's status says about its phase.
	message func(obj *A) string
	// failedDetail, when set, explains a failure of op ("create" or
	// "update") instead of managedResource.failedDetail.
	failedDetail func(op string, m *M, obj *A) string
	// timedOutDetail, when set, is added to the warning given when the
	// timeout fires first.
	timedOutDetail func(m *M, obj *A) string
	// notApplied, when set, reports that the platform has not applied obj's
	// latest change and is failing to. An update timeout in that state is
	// an error, not a warning: the change is in state but not in effect.
	notApplied func(obj *A) bool
}

// reconcileFailing reports that the platform has not applied generation
// (observedGeneration is behind it) and its Ready condition says it failed
// to and is retrying.
func reconcileFailing(generation, observedGeneration int64, conditions []client.Condition) bool {
	return observedGeneration < generation &&
		client.ConditionReason(conditions, client.ConditionReady) == client.ReasonReconcileFailed
}

func (r *managedResource[M, A]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (r *managedResource[M, A]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state M
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, etag, err := r.kind.get(ctx, r.client, &state)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read "+r.kind.noun, apiErrorDetail(err))
		return
	}

	r.kind.mapToState(obj, etag, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ImportState imports by name. A resource whose ID says more shadows it.
func (r *managedResource[M, A]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// created records obj, which the API just created, so an interrupted apply
// still tracks it, then waits for it to be ready.
func (r *managedResource[M, A]) created(ctx context.Context, obj *A, etag string, plan *M, state *tfsdk.State, diags *diag.Diagnostics) {
	r.kind.mapToState(obj, etag, plan)
	diags.Append(state.Set(ctx, plan)...)
	if diags.HasError() {
		return
	}
	timeout, d := r.kind.timeoutsOf(plan).Create(ctx, r.kind.defaults.create)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	r.waitForReady(ctx, timeout, "create", obj, plan, state, diags)
}

// patchFunc sends an update with an If-Match ETag.
type patchFunc[A any] func(ctx context.Context, etag string) (*A, string, error)

// update applies a planned change. send is nil when nothing the API holds
// changed (only provider-side settings such as timeouts): the resource is
// refreshed instead. Otherwise send runs through updateWithRetry with
// priorETag, the result is recorded, and when wait is set Update waits for
// the change to take effect.
func (r *managedResource[M, A]) update(
	ctx context.Context, plan, prior *M, priorETag string, send patchFunc[A], wait bool,
	state *tfsdk.State, diags *diag.Diagnostics,
) {
	if send == nil {
		obj, etag, err := r.kind.get(ctx, r.client, plan)
		if err != nil {
			diags.AddError("failed to read "+r.kind.noun, apiErrorDetail(err))
			return
		}
		r.kind.mapToState(obj, etag, plan)
		diags.Append(state.Set(ctx, plan)...)
		return
	}

	obj, etag, err := r.updateWithRetry(ctx, prior, priorETag, send)
	var changed *concurrentChangeError
	if errors.As(err, &changed) {
		diags.AddError(r.kind.noun+" changed outside this apply",
			fmt.Sprintf("%s %q was changed by another client since Terraform last read it (%s now differs "+
				"from state), so the update was not applied. Run `terraform plan` to review the current values, then "+
				"apply again.", capitalise(r.kind.noun), r.kind.name(plan), strings.Join(changed.fields, ", ")))
		return
	}
	var apiErr *client.APIError
	if client.IsPreconditionFailed(err) && errors.As(err, &apiErr) {
		// Only reached after the retry: the resource kept changing between
		// the re-read and the second attempt.
		diags.AddError("failed to update "+r.kind.noun, apiErrorSummary(apiErr)+
			fmt.Sprintf("\n\nThe %s kept changing while the update was being sent, so it was not applied. Run "+
				"`terraform apply` again.", r.kind.noun))
		return
	}
	if err != nil {
		diags.AddError("failed to update "+r.kind.noun, apiErrorDetail(err))
		return
	}
	r.kind.mapToState(obj, etag, plan)
	diags.Append(state.Set(ctx, plan)...)
	if diags.HasError() || !wait {
		return
	}

	timeout, d := r.kind.timeoutsOf(plan).Update(ctx, r.kind.defaults.update)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	r.waitForReady(ctx, timeout, "update", obj, plan, state, diags)
}

// concurrentChangeError reports the settings another client changed while an
// update was in flight.
type concurrentChangeError struct {
	fields []string
}

func (e *concurrentChangeError) Error() string {
	return "changed concurrently: " + strings.Join(e.fields, ", ")
}

// updateWithRetry sends the update with the ETag from state. The ETag
// changes on every write, including the platform's own status updates, so a
// 412 often means only the status moved. On a 412 it re-reads the resource:
// when its settings still match prior, nobody else changed them and the
// update is sent once more with the fresh ETag; otherwise it returns a
// *concurrentChangeError. Every setting is compared, not only the changed
// ones, because the response to the retried update becomes the new state and
// must match the plan.
func (r *managedResource[M, A]) updateWithRetry(ctx context.Context, prior *M, etag string, send patchFunc[A]) (*A, string, error) {
	obj, newETag, err := send(ctx, etag)
	if !client.IsPreconditionFailed(err) || r.kind.settingsChanged == nil {
		return obj, newETag, err
	}
	current, currentETag, getErr := r.kind.get(ctx, r.client, prior)
	if getErr != nil {
		return nil, "", fmt.Errorf("re-reading %s after a stale update: %w", r.kind.noun, getErr)
	}
	if fields := r.kind.settingsChanged(prior, current); len(fields) > 0 {
		return nil, "", &concurrentChangeError{fields: fields}
	}
	return send(ctx, currentETag)
}

// errReadyFailed ends waitForReady when the resource failed.
var errReadyFailed = errors.New("failed while waiting")

// waitForReady polls until the resource converges, saving each observation
// to state so an interrupted apply keeps the latest values. op ("create" or
// "update") names the operation in diagnostics; obj is the last observation
// before the wait. A failed resource ends the wait with an error once its
// grace has passed. A timeout is a warning while the resource is still
// progressing, since the platform carries on; it is an error while the
// resource is failing, or on update while the platform fails to apply the
// change.
func (r *managedResource[M, A]) waitForReady(
	ctx context.Context, timeout time.Duration, op string, obj *A, m *M, state *tfsdk.State, diags *diag.Diagnostics,
) {
	ready := r.kind.ready
	last := obj
	var failingSince time.Time
	err := pollUntil(ctx, timeout, func(c context.Context) (bool, error) {
		observed, etag, err := r.kind.get(c, r.client, m)
		if err != nil {
			return false, err
		}
		last = observed
		r.kind.mapToState(observed, etag, m)
		diags.Append(state.Set(c, m)...)
		if ready.converged(observed) {
			return true, nil
		}
		grace, failed := time.Duration(0), false
		if ready.failed != nil {
			grace, failed = ready.failed(observed)
		}
		if !failed {
			failingSince = time.Time{}
			return false, nil
		}
		if failingSince.IsZero() {
			failingSince = time.Now()
		}
		if time.Since(failingSince) >= grace {
			return true, errReadyFailed
		}
		return false, nil
	})
	phase := ready.phase(last)
	switch {
	case err == nil:
		// ready
	case errors.Is(err, errReadyFailed):
		diags.AddError(r.kind.noun+" "+strings.ToLower(phase), r.failedDetail(op, m, last))
	case isTerminalAPIError(err):
		diags.AddError(fmt.Sprintf("%s %s failed while waiting for %s", r.kind.noun, op, ready.target), apiErrorDetail(err))
	case !failingSince.IsZero():
		// The timeout fired within the grace: the resource is still in a
		// failed state, which is not progress.
		diags.AddError(r.kind.noun+" "+strings.ToLower(phase), r.failedDetail(op, m, last)+
			fmt.Sprintf("\n\nThe %s timeout fired while it was %s.", op, phase))
	case op == "update" && ready.notApplied != nil && ready.notApplied(last):
		// Not progress either: the platform keeps failing to apply the
		// change, and the next plan would show nothing to do.
		diags.AddError(r.kind.noun+" update not applied", r.notAppliedDetail(m, last))
	default:
		detail := fmt.Sprintf("%s %q was at phase=%q when the %s timeout fired. The Kupe platform continues in "+
			"the background; re-run `terraform apply` later and it will be a no-op once the %s is %s. If it stays "+
			"in this phase, check it in the Kupe Console or contact Kupe Cloud support. Override with "+
			"`timeouts.%s = %q`.",
			capitalise(r.kind.noun), r.kind.name(m), phase, op, r.kind.noun, ready.target, op,
			longerTimeout(timeout, r.kind.defaults.of(op)))
		if ready.timedOutDetail != nil {
			detail += ready.timedOutDetail(m, last)
		}
		diags.AddWarning(fmt.Sprintf("%s %s timed out before reaching %s", r.kind.noun, op, ready.target), detail)
	}
}

// phaseTerminating is the phase of a resource being deleted.
const phaseTerminating = "Terminating"

// failedDetail explains why waiting for obj ended in failure: its phase and
// status message, what to do, and on create that Terraform keeps it in
// state, marked tainted.
func (r *managedResource[M, A]) failedDetail(op string, m *M, obj *A) string {
	ready := r.kind.ready
	if ready.failedDetail != nil {
		return ready.failedDetail(op, m, obj)
	}
	phase := ready.phase(obj)
	detail := fmt.Sprintf("%s %q is %s, so the %s did not complete.", capitalise(r.kind.noun), r.kind.name(m), phase, op)
	if ready.message != nil {
		if msg := ready.message(obj); msg != "" {
			detail += "\n\nStatus message: " + msg
		}
	}
	if phase == phaseTerminating {
		return detail + "\n\nIt is being deleted, so it can no longer change. Once it is gone, " +
			"`terraform apply` creates it again; remove it from the configuration if that is not wanted."
	}
	detail += "\n\nThe Kupe platform keeps trying to recover it. If it stays in this phase, check it in the " +
		"Kupe Console or contact Kupe Cloud support."
	switch op {
	case "create":
		detail += fmt.Sprintf("\n\nTerraform keeps the %s in state, marked tainted, so the next `terraform apply` "+
			"plans to replace it. To keep it instead, run `terraform untaint` on it.", r.kind.noun)
	case "update":
		detail += "\n\n" + changeRecordedNotice
	}
	return detail
}

// changeRecordedNotice tells the user that an update which failed or did not
// finish is already in state: Update records what the API accepted before it
// waits, so the next plan shows nothing to do and the change is not sent
// again. The platform holds the change too, so a refresh does not bring the
// old value back.
const changeRecordedNotice = "The change was accepted and is recorded in Terraform state, so running " +
	"`terraform apply` again plans no change and does not send it again. To back the change out, set the " +
	"previous value in the configuration and apply."

// notAppliedDetail explains an update the platform was still failing to
// apply when the timeout fired.
func (r *managedResource[M, A]) notAppliedDetail(m *M, obj *A) string {
	ready := r.kind.ready
	detail := fmt.Sprintf("The Kupe platform has not applied the change to %s %q: it failed to and was still "+
		"retrying when the update timeout fired.", r.kind.noun, r.kind.name(m))
	if ready.message != nil {
		if msg := ready.message(obj); msg != "" {
			detail += "\n\nStatus message: " + msg
		}
	}
	return detail + "\n\nThe Kupe platform keeps retrying it. If it is not applied, check the " + r.kind.noun +
		" in the Kupe Console or contact Kupe Cloud support.\n\n" + changeRecordedNotice
}

func (r *managedResource[M, A]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state M
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := r.kind.timeoutsOf(&state).Delete(ctx, r.kind.defaults.delete)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.deleteAndConfirm(ctx, timeout, &state, &resp.Diagnostics)
}

// deleteAndConfirm sends the delete until the API accepts it, then waits
// until the resource is gone. The platform tears a resource down after
// accepting the delete and keeps its name reserved until it is gone, so an
// immediate re-create would conflict. A timeout is a warning: Terraform
// drops the resource from state and the platform finishes in the background.
func (r *managedResource[M, A]) deleteAndConfirm(ctx context.Context, timeout time.Duration, m *M, diags *diag.Diagnostics) {
	// One deadline covers both sending the delete and waiting for it.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := deleteUntilAccepted(ctx, timeout, func(c context.Context) error {
		return r.kind.del(c, r.client, m)
	})
	if r.kind.deleteRefused != nil {
		if summary, detail, refused := r.kind.deleteRefused(m, err); refused {
			diags.AddError(summary, detail)
			return
		}
	}
	if err != nil {
		diags.AddError("failed to delete "+r.kind.noun, deleteErrorDetail(err))
		return
	}

	err = waitForDeletion(ctx, timeout, func(c context.Context) error {
		_, _, err := r.kind.get(c, r.client, m)
		return err
	})
	switch {
	case err == nil:
		// confirmed gone
	case isTerminalAPIError(err):
		diags.AddError("failed to confirm "+r.kind.noun+" deletion", apiErrorDetail(err))
	default:
		diags.AddWarning(r.kind.noun+" still terminating", r.kind.stillDeleting(m))
	}
}

// longerTimeout is the timeout diagnostics suggest: twice the one that
// fired or twice the default, whichever is longer, in minutes.
func longerTimeout(fired, def time.Duration) string {
	return fmt.Sprintf("%dm", int((2 * max(fired, def)).Minutes()))
}

// capitalise upper-cases the first letter of an ASCII noun.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
