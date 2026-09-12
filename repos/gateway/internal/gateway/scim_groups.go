package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
)

const (
	scimGroupSchema      = "urn:ietf:params:scim:schemas:core:2.0:Group"
	scimGroupResourceURL = "/scim/v2/Groups/"
)

var (
	scimGroupFilterPattern = regexp.MustCompile(`(?i)^\s*(displayName|externalId)\s+eq\s+("(?:[^"\\]|\\.)*")\s*$`)
	scimMemberPathPattern  = regexp.MustCompile(`(?i)^members\s*\[\s*value\s+eq\s+("(?:[^"\\]|\\.)*")\s*\]$`)
)

type scimMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

type scimGroup struct {
	Schemas     []string     `json:"schemas"`
	ID          string       `json:"id,omitempty"`
	ExternalID  string       `json:"externalId,omitempty"`
	DisplayName string       `json:"displayName"`
	Members     []scimMember `json:"members,omitempty"`
	Meta        *scimMeta    `json:"meta,omitempty"`
}

func (h Handler) ListSCIMGroups(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	start, count, ok := scimPagination(w, r)
	if !ok {
		return
	}
	attribute, value, filtered, ok := parseSCIMGroupFilter(w, r.URL.Query().Get("filter"))
	if !ok {
		return
	}
	var teams []DirectoryTeam
	var total int
	var err error
	if filtered {
		var team DirectoryTeam
		var found bool
		team, found, err = h.directory.FindGroup(r.Context(), managementAudit(req), attribute, value)
		if found {
			total = 1
			if start == 1 && count > 0 {
				var group DirectoryGroup
				group, err = h.directory.GetGroup(r.Context(), managementAudit(req), team.ID)
				if err == nil {
					team, team.MemberIDs = group.Team, group.Members
				}
				teams = []DirectoryTeam{team}
			}
		}
	} else {
		limit := count
		if limit == 0 {
			limit = 1
		}
		teams, total, err = h.directory.ListTeams(r.Context(), managementAudit(req), "", start-1, limit, false)
	}
	if err != nil {
		writeSCIMGroupFailure(w, err)
		return
	}
	resources := make([]any, 0, len(teams))
	for _, team := range teams {
		resources = append(resources, scimGroupFromDirectory(team, team.MemberIDs))
	}
	writeSCIMJSON(w, http.StatusOK, scimListResponse(resources, total, start, len(resources)))
}

func (h Handler) GetSCIMGroup(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	group, err := h.directory.GetGroup(r.Context(), managementAudit(req), r.PathValue("id"))
	if err != nil {
		writeSCIMGroupFailure(w, err)
		return
	}
	if group.Team.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "group not found")
		return
	}
	writeSCIMJSON(w, http.StatusOK, scimGroupFromDirectory(group.Team, group.Members))
}

func (h Handler) CreateSCIMGroup(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var input scimGroup
	if !decodeSCIMJSON(w, r, &input) || !validSCIMGroupInput(w, input) {
		return
	}
	id, ok := newSCIMResourceID("grp_")
	if !ok {
		writeSCIMError(w, http.StatusServiceUnavailable, "", "could not allocate group identifier")
		return
	}
	group := scimGroupToDirectory(id, input)
	h.saveSCIMGroup(w, r, req, group, true, "scim.group.create")
}

func (h Handler) ReplaceSCIMGroup(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var input scimGroup
	if !decodeSCIMJSON(w, r, &input) || !validSCIMGroupInput(w, input) {
		return
	}
	id := r.PathValue("id")
	current, err := h.directory.GetGroup(r.Context(), managementAudit(req), id)
	if err != nil {
		writeSCIMGroupFailure(w, err)
		return
	}
	if current.Team.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "group not found")
		return
	}
	h.saveSCIMGroup(w, r, req, scimGroupToDirectory(id, input), false, "scim.group.replace")
}

func (h Handler) PatchSCIMGroup(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var patch scimPatchRequest
	if !decodeSCIMJSON(w, r, &patch) {
		return
	}
	if len(patch.Operations) == 0 || len(patch.Operations) > 32 || len(patch.Schemas) != 1 || patch.Schemas[0] != scimPatchSchema {
		writeSCIMError(w, http.StatusBadRequest, "invalidSyntax", "invalid patch request")
		return
	}
	current, err := h.directory.GetGroup(r.Context(), managementAudit(req), r.PathValue("id"))
	if err != nil {
		writeSCIMGroupFailure(w, err)
		return
	}
	if current.Team.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "group not found")
		return
	}
	input := scimGroupFromDirectory(current.Team, current.Members)
	input.ID, input.Meta = "", nil
	for _, operation := range patch.Operations {
		if err := applySCIMGroupPatch(&input, operation); err != nil {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
			return
		}
	}
	if !validSCIMGroupInput(w, input) {
		return
	}
	h.saveSCIMGroup(w, r, req, scimGroupToDirectory(current.Team.ID, input), false, "scim.group.patch")
}

func (h Handler) DeleteSCIMGroup(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	current, err := h.directory.GetGroup(r.Context(), managementAudit(req), r.PathValue("id"))
	if err != nil {
		writeSCIMGroupFailure(w, err)
		return
	}
	if current.Team.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "group not found")
		return
	}
	now := time.Now().UTC()
	current.Team.DeletedAt, current.Team.Status = &now, "disabled"
	current.Members = nil
	h.saveSCIMGroup(w, r, req, current, false, "scim.group.delete")
}

func (h Handler) saveSCIMGroup(w http.ResponseWriter, r *http.Request, req modules.RequestContext, group DirectoryGroup, create bool, action string) {
	audit := managementAudit(req)
	event := AuditEvent{Action: action, TargetType: "team", TargetID: group.Team.ID, Details: map[string]any{"member_count": len(group.Members)}}
	if !h.auditMutation(r.Context(), audit, event) {
		writeSCIMError(w, http.StatusServiceUnavailable, "", "audit service is unavailable")
		return
	}
	saved, err := h.directory.SaveGroup(r.Context(), audit, group, create)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeSCIMGroupFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	result := scimGroupFromDirectory(saved.Team, saved.Members)
	if create {
		w.Header().Set("Location", result.Meta.Location)
		writeSCIMJSON(w, http.StatusCreated, result)
		return
	}
	writeSCIMJSON(w, http.StatusOK, result)
}

func scimGroupFromDirectory(team DirectoryTeam, memberIDs []string) scimGroup {
	members := make([]scimMember, 0, len(memberIDs))
	for _, id := range memberIDs {
		members = append(members, scimMember{Value: id, Ref: scimResourceBaseURL + url.PathEscape(id)})
	}
	return scimGroup{Schemas: []string{scimGroupSchema}, ID: team.ID, ExternalID: team.ExternalID, DisplayName: team.Name, Members: members, Meta: &scimMeta{ResourceType: "Group", Created: team.CreatedAt, LastModified: team.UpdatedAt, Location: scimGroupResourceURL + url.PathEscape(team.ID)}}
}

func scimGroupToDirectory(id string, group scimGroup) DirectoryGroup {
	members := make([]string, 0, len(group.Members))
	for _, member := range group.Members {
		members = append(members, strings.TrimSpace(member.Value))
	}
	return DirectoryGroup{Team: DirectoryTeam{ID: id, ExternalID: strings.TrimSpace(group.ExternalID), Name: strings.TrimSpace(group.DisplayName), Status: "active"}, Members: members}
}

func validSCIMGroupInput(w http.ResponseWriter, group scimGroup) bool {
	if len(group.Schemas) != 1 || group.Schemas[0] != scimGroupSchema || strings.TrimSpace(group.DisplayName) == "" || len(group.DisplayName) > 256 || len(group.ExternalID) > 256 || len(group.Members) > 500 {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "invalid group resource")
		return false
	}
	for _, member := range group.Members {
		if strings.TrimSpace(member.Value) == "" || len(member.Value) > 256 {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "invalid group member")
			return false
		}
	}
	return true
}

func applySCIMGroupPatch(group *scimGroup, operation scimPatchOperation) error {
	op, path := strings.ToLower(strings.TrimSpace(operation.Op)), strings.TrimSpace(operation.Path)
	lowerPath := strings.ToLower(path)
	if op == "remove" {
		match := scimMemberPathPattern.FindStringSubmatch(path)
		if len(match) != 2 {
			return fmt.Errorf("remove path %q is not supported", path)
		}
		value, err := strconv.Unquote(match[1])
		if err != nil {
			return errors.New("invalid member filter")
		}
		members := group.Members[:0]
		for _, member := range group.Members {
			if member.Value != value {
				members = append(members, member)
			}
		}
		group.Members = members
		return nil
	}
	if op != "add" && op != "replace" {
		return fmt.Errorf("operation %q is not supported", operation.Op)
	}
	if lowerPath == "" {
		var values struct {
			ExternalID  *string       `json:"externalId"`
			DisplayName *string       `json:"displayName"`
			Members     *[]scimMember `json:"members"`
		}
		if err := decodeSCIMRaw(operation.Value, &values); err != nil {
			return errors.New("patch value must be an object")
		}
		if values.ExternalID == nil && values.DisplayName == nil && values.Members == nil {
			return errors.New("patch value has no supported attributes")
		}
		if values.ExternalID != nil {
			group.ExternalID = *values.ExternalID
		}
		if values.DisplayName != nil {
			group.DisplayName = *values.DisplayName
		}
		if values.Members != nil {
			if op == "add" {
				group.Members = appendUniqueSCIMMembers(group.Members, *values.Members...)
			} else {
				group.Members = *values.Members
			}
		}
		return nil
	}
	switch lowerPath {
	case "externalid":
		return json.Unmarshal(operation.Value, &group.ExternalID)
	case "displayname":
		return json.Unmarshal(operation.Value, &group.DisplayName)
	case "members":
		var members []scimMember
		if err := json.Unmarshal(operation.Value, &members); err != nil {
			return err
		}
		if op == "add" {
			group.Members = appendUniqueSCIMMembers(group.Members, members...)
		} else {
			group.Members = members
		}
		return nil
	default:
		return fmt.Errorf("path %q is not supported", operation.Path)
	}
}

func appendUniqueSCIMMembers(existing []scimMember, added ...scimMember) []scimMember {
	seen := make(map[string]struct{}, len(existing)+len(added))
	result := make([]scimMember, 0, len(existing)+len(added))
	for _, member := range append(append([]scimMember(nil), existing...), added...) {
		if _, ok := seen[member.Value]; ok {
			continue
		}
		seen[member.Value] = struct{}{}
		result = append(result, member)
	}
	return result
}

func parseSCIMGroupFilter(w http.ResponseWriter, raw string) (string, string, bool, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", "", false, true
	}
	match := scimGroupFilterPattern.FindStringSubmatch(raw)
	if len(match) != 3 {
		writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "only displayName eq and externalId eq filters are supported")
		return "", "", false, false
	}
	value, err := strconv.Unquote(match[2])
	if err != nil || strings.TrimSpace(value) == "" || len(value) > 256 {
		writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "invalid filter value")
		return "", "", false, false
	}
	attribute := "displayName"
	if strings.EqualFold(match[1], "externalId") {
		attribute = "externalId"
	}
	return attribute, value, true, true
}

func writeSCIMGroupFailure(w http.ResponseWriter, err error) {
	var managementErr *ManagementError
	if errors.As(err, &managementErr) {
		switch managementErr.Status {
		case http.StatusBadRequest:
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "invalid group resource")
			return
		case http.StatusNotFound:
			writeSCIMError(w, http.StatusNotFound, "", "group or member not found")
			return
		case http.StatusConflict:
			writeSCIMError(w, http.StatusConflict, "uniqueness", "group already exists")
			return
		}
	}
	writeSCIMError(w, http.StatusServiceUnavailable, "", "identity directory is unavailable")
}
