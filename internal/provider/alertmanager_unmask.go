package provider

import "encoding/json"

// Client-side unmasking of kupe-api's alertmanager secret masking.
//
// Since kupe-api KA-1, every credential-bearing value in a receiver or
// global body (webhook URLs, passwords, tokens, *_key fields, …) comes
// back from GET and PUT responses replaced by the sentinel "<secret>".
// The provider therefore must never store a server echo verbatim:
//
//   - On Create/Update the masked echo carries no information the plan
//     does not already have, so the resources keep the planned body_json
//     and take only the ETag from the response.
//   - On Read the fetched body is unmasked against the prior state's
//     body_json before storing: wherever the server reports the sentinel,
//     the value recorded at the last successful write is substituted.
//     This mirrors kupe-api's own UnmaskAgainst (internal/alertmanager/
//     mask.go) so drift on non-secret fields is still detected while
//     masked secret fields cannot produce false drift.
//
// The sentinel value must stay in sync with kupe-api's
// alertmanager.SecretSentinel.
const secretSentinel = "<secret>"

// unmaskAgainstPrior walks a freshly-fetched (masked) config value and,
// wherever it finds the secret sentinel, substitutes the corresponding
// value from prior (the body recorded in Terraform state at the last
// write). Structural shape is always taken from next, so fields added or
// removed server-side still surface as drift.
//
// Unlike kupe-api's UnmaskAgainst — which drops an unmatched sentinel so
// it can never be persisted as a live credential — an unmatched sentinel
// is kept here: state is never written back to the server (writes always
// come from the user's config), and keeping it makes a secret field that
// was added outside Terraform show up as drift instead of being hidden.
//
// The substitution is value-driven rather than key-driven on purpose:
// duplicating kupe-api's secret-key fragment list would silently rot as
// the API adds fragments, whereas matching on the sentinel automatically
// tracks whatever the server masks.
func unmaskAgainstPrior(next, prior any) any {
	switch t := next.(type) {
	case map[string]any:
		priorMap, _ := prior.(map[string]any)
		out := make(map[string]any, len(t))
		for k, v := range t {
			var pv any
			if priorMap != nil {
				pv = priorMap[k]
			}
			out[k] = unmaskAgainstPrior(v, pv)
		}
		return out
	case []any:
		priorSlice, _ := prior.([]any)
		out := make([]any, len(t))
		for i, v := range t {
			var pv any
			if priorSlice != nil && i < len(priorSlice) {
				pv = priorSlice[i]
			}
			out[i] = unmaskAgainstPrior(v, pv)
		}
		return out
	case string:
		if t == secretSentinel && prior != nil {
			return prior
		}
		return t
	default:
		return next
	}
}

// unmaskBodyAgainstState parses the prior state's body_json and unmasks a
// fetched (masked) body map against it. If the prior body is empty or
// unparsable — e.g. during import, where no prior state exists — the
// fetched map is returned unchanged and any sentinels stay in place.
func unmaskBodyAgainstState(fetched map[string]any, priorBodyJSON string) map[string]any {
	if fetched == nil || priorBodyJSON == "" {
		return fetched
	}
	prior := map[string]any{}
	if err := json.Unmarshal([]byte(priorBodyJSON), &prior); err != nil {
		return fetched
	}
	out, _ := unmaskAgainstPrior(fetched, prior).(map[string]any)
	return out
}
