package gateway

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

type PolicyResolutionRequest struct {
	OrganizationID  string   `json:"organization_id,omitempty"`
	TeamID          string   `json:"team_id,omitempty"`
	UserID          string   `json:"user_id,omitempty"`
	CredentialID    string   `json:"credential_id,omitempty"`
	CredentialAlias string   `json:"credential_alias,omitempty"`
	Model           string   `json:"model,omitempty"`
	ProviderID      string   `json:"provider_id,omitempty"`
	DeploymentID    string   `json:"deployment_id,omitempty"`
	Tags            []string `json:"tags,omitempty"`
}

type ResolvedPolicyAttachment struct {
	ID                 string   `json:"id"`
	PolicyName         string   `json:"policy_name"`
	Scope              string   `json:"scope"`
	MatchedVia         []string `json:"matched_via"`
	PolicyStatus       string   `json:"policy_status"`
	DLP                bool     `json:"dlp"`
	OutputDLP          bool     `json:"output_dlp"`
	AV                 bool     `json:"av"`
	Anonymization      string   `json:"anonymization,omitempty"`
	AnonymizationRules []string `json:"anonymization_rules,omitempty"`
}

type PolicyResolutionIssue struct {
	AttachmentID string `json:"attachment_id"`
	PolicyName   string `json:"policy_name"`
	Code         string `json:"code"`
}

type PolicyResolutionResponse struct {
	MatchedAttachments    []ResolvedPolicyAttachment `json:"matched_attachments"`
	EffectivePolicies     []string                   `json:"effective_policies"`
	DLP                   bool                       `json:"dlp"`
	OutputDLP             bool                       `json:"output_dlp"`
	AV                    bool                       `json:"av"`
	Anonymization         string                     `json:"anonymization,omitempty"`
	AnonymizationRules    []string                   `json:"anonymization_rules,omitempty"`
	AnonymizationProfiles []string                   `json:"anonymization_profiles,omitempty"`
	Enforceable           bool                       `json:"enforceable"`
	Issues                []PolicyResolutionIssue    `json:"issues"`
}

func (h Handler) ResolvePolicyAttachments(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "policy attachment registry is unavailable")
		return
	}
	var input PolicyResolutionRequest
	if !decodeAccessJSON(w, r, &input) {
		return
	}
	input.OrganizationID = strings.TrimSpace(input.OrganizationID)
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.CredentialID = strings.TrimSpace(input.CredentialID)
	input.CredentialAlias = strings.TrimSpace(input.CredentialAlias)
	input.Model = strings.TrimSpace(input.Model)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.DeploymentID = strings.TrimSpace(input.DeploymentID)
	input.Tags = uniqueStrings(input.Tags)
	if len(input.OrganizationID) > 512 || len(input.TeamID) > 512 || len(input.UserID) > 512 || len(input.CredentialID) > 512 || len(input.CredentialAlias) > 512 || len(input.Model) > 512 || len(input.ProviderID) > 512 || len(input.DeploymentID) > 512 || !validAccessStrings(input.Tags) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid policy resolution context")
		return
	}
	context := PolicyMatchContext{OrganizationID: input.OrganizationID, TeamID: input.TeamID, UserID: input.UserID, CredentialID: input.CredentialID, CredentialAlias: input.CredentialAlias, Model: input.Model, ProviderID: input.ProviderID, DeploymentID: input.DeploymentID, Tags: input.Tags}
	writeJSON(w, http.StatusOK, h.resolvePolicyAttachmentSet(h.access.MatchingPolicyAttachments(context), context))
}

func (h Handler) resolvePolicyAttachmentSet(attachments []PolicyAttachment, context PolicyMatchContext) PolicyResolutionResponse {
	result := PolicyResolutionResponse{
		MatchedAttachments: make([]ResolvedPolicyAttachment, 0, len(attachments)),
		EffectivePolicies:  []string{},
		Enforceable:        true,
		Issues:             []PolicyResolutionIssue{},
	}
	controller, controllerAvailable := h.guardrailController()
	seen := make(map[string]struct{}, len(attachments))
	anonymizationSettings := make([]provider.AnonymizationSetting, 0, len(attachments))
	for _, attachment := range attachments {
		resolved := ResolvedPolicyAttachment{ID: attachment.ID, PolicyName: attachment.PolicyName, Scope: attachment.Scope, MatchedVia: policyAttachmentMatchDimensions(attachment, context)}
		if !controllerAvailable {
			resolved.PolicyStatus = "unavailable"
			result.Enforceable = false
			result.Issues = append(result.Issues, PolicyResolutionIssue{AttachmentID: attachment.ID, PolicyName: attachment.PolicyName, Code: "policy_controller_unavailable"})
			result.MatchedAttachments = append(result.MatchedAttachments, resolved)
			continue
		}
		policy, found := controller.GetGuardrailPolicy(attachment.PolicyName)
		if !found {
			resolved.PolicyStatus = "missing"
			result.Enforceable = false
			result.Issues = append(result.Issues, PolicyResolutionIssue{AttachmentID: attachment.ID, PolicyName: attachment.PolicyName, Code: "policy_missing"})
		} else if !policy.Enabled {
			resolved.PolicyStatus = "disabled"
			resolved.DLP, resolved.OutputDLP, resolved.AV = policy.DLP, policy.OutputDLP, policy.AV
			resolved.Anonymization, resolved.AnonymizationRules = policy.Anonymization, append([]string(nil), policy.AnonymizationRules...)
			result.Enforceable = false
			result.Issues = append(result.Issues, PolicyResolutionIssue{AttachmentID: attachment.ID, PolicyName: attachment.PolicyName, Code: "policy_disabled"})
		} else {
			resolved.PolicyStatus = "enabled"
			resolved.DLP, resolved.OutputDLP, resolved.AV = policy.DLP, policy.OutputDLP, policy.AV
			resolved.Anonymization, resolved.AnonymizationRules = policy.Anonymization, append([]string(nil), policy.AnonymizationRules...)
			result.DLP = result.DLP || policy.DLP
			result.OutputDLP = result.OutputDLP || policy.OutputDLP
			result.AV = result.AV || policy.AV
			if policy.Anonymization != "" {
				anonymizationSettings = append(anonymizationSettings, provider.AnonymizationSetting{Profile: policy.Name, Mode: policy.Anonymization, Rules: policy.AnonymizationRules})
			}
			if _, duplicate := seen[policy.Name]; !duplicate {
				seen[policy.Name] = struct{}{}
				result.EffectivePolicies = append(result.EffectivePolicies, policy.Name)
			}
		}
		result.MatchedAttachments = append(result.MatchedAttachments, resolved)
	}
	sort.Strings(result.EffectivePolicies)
	if len(anonymizationSettings) != 0 {
		result.Anonymization, result.AnonymizationRules, result.AnonymizationProfiles = provider.ResolveAnonymization(anonymizationSettings...)
	}
	return result
}

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
		for _, attachment := range h.access.CandidatePolicyAttachments(PolicyMatchContext{
			OrganizationID:  req.OrganizationID,
			TeamID:          req.TeamID,
			UserID:          req.UserID,
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
	validation := h.resolvePolicyAttachmentSet(attachments, PolicyMatchContext{})
	if !validation.Enforceable {
		message := "an attached policy is missing or disabled"
		for _, issue := range validation.Issues {
			if issue.Code == "policy_controller_unavailable" {
				message = "required policy evaluation is unavailable"
				break
			}
		}
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable", message)
		return false
	}
	immediate := make([]PolicyAttachment, 0, len(attachments))
	deferred := make([]PolicyAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if len(attachment.Providers) != 0 || len(attachment.Deployments) != 0 {
			deferred = append(deferred, attachment)
		} else {
			immediate = append(immediate, attachment)
		}
	}
	resolution := h.resolvePolicyAttachmentSet(immediate, PolicyMatchContext{})
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["policy.guardrail.required"] = "true"
	req.Metadata["policy.guardrail.names"] = strings.Join(resolution.EffectivePolicies, ",")
	req.Metadata["policy.modules.dlp.enabled"] = strconv.FormatBool(resolution.DLP)
	req.Metadata["policy.modules.dlp.output_enabled"] = strconv.FormatBool(resolution.OutputDLP)
	req.Metadata["policy.modules.av.enabled"] = strconv.FormatBool(resolution.AV)
	if resolution.Anonymization != "" {
		req.Metadata["policy.modules.anonymizer.mode"] = resolution.Anonymization
		req.Metadata["policy.modules.anonymizer.rules"] = strings.Join(resolution.AnonymizationRules, ",")
		req.Metadata["policy.modules.anonymizer.profiles"] = strings.Join(resolution.AnonymizationProfiles, ",")
	}
	if len(deferred) != 0 {
		controller, _ := h.guardrailController()
		endpointAttachments := make([]provider.EndpointPolicyAttachment, 0, len(deferred))
		for _, attachment := range deferred {
			policy, _ := controller.GetGuardrailPolicy(attachment.PolicyName)
			endpointAttachments = append(endpointAttachments, provider.EndpointPolicyAttachment{
				PolicyName: attachment.PolicyName, Providers: attachment.Providers, Deployments: attachment.Deployments,
				DLP: policy.DLP, OutputDLP: policy.OutputDLP, AV: policy.AV,
				Anonymization: policy.Anonymization, AnonymizationRules: policy.AnonymizationRules,
			})
		}
		encoded, err := json.Marshal(endpointAttachments)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "policy_unavailable", "required policy evaluation is unavailable")
			return false
		}
		req.Metadata[provider.EndpointPolicyAttachmentsMetadataKey] = string(encoded)
	}
	return true
}
