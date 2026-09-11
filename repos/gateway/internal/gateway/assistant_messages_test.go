package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
)

func TestAssistantMessageLifecycleOwnershipPaginationAndQuota(t *testing.T) {
	store := &memoryAssistantThreadStore{
		memoryAssistantStore: &memoryAssistantStore{records: map[string]assistantstate.Record{}},
		threads:              map[string]assistantstate.ThreadRecord{},
		messages:             map[string]assistantstate.MessageRecord{},
	}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user-a"})
	files := &memoryFileStore{files: map[string]filestate.File{"file_owned": {ID: "file_owned", OwnerKey: owner, Purpose: "assistants"}}}
	handlerFor := func(user string) http.Handler {
		return Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: user}}), nil).
			WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
			WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 10, ThreadOwnerQuota: 10, MessageThreadQuota: 2}))
	}
	handler := handlerFor("user-a")
	threadResponse := assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{}`)
	var thread map[string]any
	if threadResponse.Code != http.StatusOK || json.Unmarshal(threadResponse.Body.Bytes(), &thread) != nil {
		t.Fatalf("thread status=%d body=%s", threadResponse.Code, threadResponse.Body.String())
	}
	threadID, _ := thread["id"].(string)
	first := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/messages", `{"role":"user","content":"hello","attachments":[{"file_id":"file_owned","tools":[{"type":"code_interpreter"}]}],"metadata":{"turn":"one"}}`)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"value":"hello"`) || !strings.Contains(first.Body.String(), `"object":"thread.message"`) {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstMessage map[string]any
	if json.Unmarshal(first.Body.Bytes(), &firstMessage) != nil {
		t.Fatal("invalid first message response")
	}
	firstID, _ := firstMessage["id"].(string)
	second := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/messages", `{"role":"assistant","content":[{"type":"image_file","image_file":{"file_id":"file_owned","detail":"low"}}]}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	page := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/messages?limit=1&order=desc", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"has_more":true`) {
		t.Fatalf("page status=%d body=%s", page.Code, page.Body.String())
	}
	ascending := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/messages?limit=2&order=asc", "")
	if ascending.Code != http.StatusOK || strings.Index(ascending.Body.String(), firstID) < 0 {
		t.Fatalf("ascending status=%d body=%s", ascending.Code, ascending.Body.String())
	}
	other := assistantRequest(t, handlerFor("user-b"), http.MethodGet, "/v1/threads/"+threadID+"/messages/"+firstID, "")
	if other.Code != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d body=%s", other.Code, other.Body.String())
	}
	updated := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/messages/"+firstID, `{"metadata":{"turn":"updated"}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"turn":"updated"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	overQuota := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/messages", `{"role":"user","content":"third"}`)
	if overQuota.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status=%d body=%s", overQuota.Code, overQuota.Body.String())
	}
	deleted := assistantRequest(t, handler, http.MethodDelete, "/v1/threads/"+threadID+"/messages/"+firstID, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestAssistantMessageRejectsInvalidContentResourcesAndPagination(t *testing.T) {
	store := &memoryAssistantThreadStore{
		memoryAssistantStore: &memoryAssistantStore{records: map[string]assistantstate.Record{}},
		threads:              map[string]assistantstate.ThreadRecord{},
		messages:             map[string]assistantstate.MessageRecord{},
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: "user"}}), nil).
		WithFileStore(&memoryFileStore{files: map[string]filestate.File{}}, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 10, ThreadOwnerQuota: 10, MessageThreadQuota: 10}))
	threadResponse := assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{}`)
	var thread map[string]any
	_ = json.Unmarshal(threadResponse.Body.Bytes(), &thread)
	threadID, _ := thread["id"].(string)
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "role", body: `{"role":"system","content":"x"}`},
		{name: "empty text", body: `{"role":"user","content":""}`},
		{name: "insecure image URL", body: `{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://example.com/a.png"}}]}`},
		{name: "unowned image file", body: `{"role":"user","content":[{"type":"image_file","image_file":{"file_id":"file_other"}}]}`},
		{name: "unknown field", body: `{"role":"user","content":"x","unknown":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/messages", test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	badPage := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/messages?after=msg_a&before=msg_b", "")
	if badPage.Code != http.StatusBadRequest {
		t.Fatalf("pagination status=%d body=%s", badPage.Code, badPage.Body.String())
	}
}
