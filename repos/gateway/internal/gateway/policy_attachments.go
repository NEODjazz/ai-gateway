package gateway

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/modules"
)

func (h Handler) applyPolicyAttachments(w http.ResponseWriter, req *modules.RequestContext, model string) bool {
	return h.applyPolicyAttachmentsForModels(w, req, []string{model})
}

func (h Handler) applyPolicyAttachmentsForModels(w http.ResponseWriter, req *modules.RequestContext, models []string) bool {
	if h.access == nil {
		return true
	}
	var attachments []PolicyAttachment
	seenAttachments := make(map[string]bool)
	for _, model := range models {
		for _, attachment := range h.access.MatchingPolicyAttachments(PolicyMatchContext{
			TeamID:          req.TeamID,
			CredentialID:    req.CredentialID,
			CredentialAlias: req.CredentialAlias,
			Model:           model,
			Tags:            req.Tags,
		}) {
			if !seenAttachments[attachment.ID] {
				seenAttachments[attachment.ID] = true
				attachments = append(attachments, attachment)
			}
		}
	}
	if len(attachments) == 0 {
		return true
	}
	controller, ok := h.guardrailController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable", "required policy evaluation is unavailable")
		return false
	}
	names := make([]string, 0, len(attachments))
	dlp, av := false, false
	seen := map[string]struct{}{}
	for _, attachment := range attachments {
		policy, found := controller.GetGuardrailPolicy(attachment.PolicyName)
		if !found || !policy.Enabled {
			writeError(w, http.StatusServiceUnavailable, "policy_unavailable", "an attached policy is missing or disabled")
			return false
		}
		dlp = dlp || policy.DLP
		av = av || policy.AV
		if _, found := seen[policy.Name]; !found {
			seen[policy.Name] = struct{}{}
			names = append(names, policy.Name)
		}
	}
	sort.Strings(names)
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["policy.guardrail.required"] = "true"
	req.Metadata["policy.guardrail.names"] = strings.Join(names, ",")
	req.Metadata["policy.modules.dlp.enabled"] = strconv.FormatBool(dlp)
	req.Metadata["policy.modules.av.enabled"] = strconv.FormatBool(av)
	return true
}
