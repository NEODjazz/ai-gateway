package gateway

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/modules"
)

const a2aPushJobKind = "a2a-push"

type a2aPushAuthentication struct {
	Schemes     []string `json:"schemes"`
	Credentials string   `json:"credentials,omitempty"`
}

type a2aPushConfig struct {
	ID             string                 `json:"id,omitempty"`
	URL            string                 `json:"url"`
	Token          string                 `json:"token,omitempty"`
	Authentication *a2aPushAuthentication `json:"authentication,omitempty"`
}

type a2aPushJobSecret struct {
	Config         a2aPushConfig `json:"config"`
	CredentialID   string        `json:"credential_id"`
	UserID         string        `json:"user_id,omitempty"`
	TeamID         string        `json:"team_id,omitempty"`
	OrganizationID string        `json:"organization_id,omitempty"`
	RequestID      string        `json:"request_id"`
}

type a2aPushJobPayload struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type a2aPushVault struct{ aead cipher.AEAD }

func newA2APushVault(keyMaterial []byte) (*a2aPushVault, error) {
	if len(keyMaterial) < 16 {
		return nil, errors.New("A2A push encryption key is too short")
	}
	key := sha256.Sum256(append([]byte("ai-gateway/a2a-push/v1\x00"), keyMaterial...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &a2aPushVault{aead: aead}, nil
}

func (h Handler) WithA2APushNotifications(store asyncstate.Store, key []byte) (Handler, error) {
	if store == nil {
		return h, errors.New("A2A push job store is unavailable")
	}
	if _, ok := h.a2aTasks.(a2astate.AtomicOutboxStore); !ok {
		return h, errors.New("A2A push requires atomic task outbox storage")
	}
	vault, err := newA2APushVault(key)
	if err != nil {
		return h, err
	}
	h.a2aPushJobs, h.a2aPushVault = store, vault
	return h, nil
}

func (c a2aPushConfig) validate() error {
	parsed, err := url.Parse(c.URL)
	if err != nil || len(c.URL) > 2048 || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || len(c.ID) > 128 || len(c.Token) > 4096 || !validA2AHeaderValue(c.Token) {
		return errors.New("invalid push URL or token")
	}
	if c.ID != "" && !validFileToken(c.ID, 128) {
		return errors.New("invalid push ID")
	}
	if c.Authentication == nil {
		return nil
	}
	if len(c.Authentication.Schemes) != 1 || c.Authentication.Credentials == "" || len(c.Authentication.Credentials) > 4096 || !validA2AHeaderValue(c.Authentication.Credentials) {
		return errors.New("invalid push authentication")
	}
	scheme := strings.ToLower(c.Authentication.Schemes[0])
	if scheme != "bearer" && scheme != "basic" {
		return errors.New("unsupported push authentication")
	}
	return nil
}

func validA2AHeaderValue(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func (h Handler) newA2APushJob(req modules.RequestContext, task a2astate.Task, config a2aPushConfig) (asyncstate.Job, error) {
	if config.ID == "" {
		config.ID = newA2AID("push")
	}
	secret := a2aPushJobSecret{Config: config, CredentialID: req.CredentialID, UserID: req.UserID, TeamID: req.TeamID, OrganizationID: req.OrganizationID, RequestID: req.RequestID}
	plaintext, err := json.Marshal(secret)
	if err != nil {
		return asyncstate.Job{}, err
	}
	nonce := make([]byte, h.a2aPushVault.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return asyncstate.Job{}, err
	}
	aad := []byte(task.OwnerKey + "\x00" + task.ID)
	payload, err := json.Marshal(a2aPushJobPayload{Nonce: nonce, Ciphertext: h.a2aPushVault.aead.Seal(nil, nonce, plaintext, aad)})
	if err != nil {
		return asyncstate.Job{}, err
	}
	return asyncstate.Job{Kind: a2aPushJobKind, ResourceID: task.ID, OwnerKey: task.OwnerKey, EndpointID: task.AgentID, ExecutionID: newA2AID("push-exec"), Payload: payload}, nil
}

func (h Handler) saveA2ATask(ctx context.Context, req modules.RequestContext, task a2astate.Task, continuation bool, expected time.Time, push *a2aPushConfig) error {
	if push != nil {
		job, err := h.newA2APushJob(req, task, *push)
		if err != nil {
			return err
		}
		store := h.a2aTasks.(a2astate.AtomicOutboxStore)
		if continuation {
			_, err = store.UpdateA2ATaskWithJob(ctx, task, expected, h.a2aTaskConfig.TTL, job)
		} else {
			_, err = store.CreateA2ATaskWithJob(ctx, task, h.a2aTaskConfig.OwnerQuota, h.a2aTaskConfig.TTL, job)
		}
		return err
	}
	if continuation {
		_, err := h.a2aTasks.UpdateA2ATask(ctx, task, expected, h.a2aTaskConfig.TTL)
		return err
	}
	_, err := h.a2aTasks.CreateA2ATask(ctx, task, h.a2aTaskConfig.OwnerQuota, h.a2aTaskConfig.TTL)
	return err
}

func (h Handler) decodeA2APushJob(job asyncstate.Job) (a2aPushJobSecret, error) {
	var payload a2aPushJobPayload
	if json.Unmarshal(job.Payload, &payload) != nil || len(payload.Nonce) != h.a2aPushVault.aead.NonceSize() || len(payload.Ciphertext) == 0 {
		return a2aPushJobSecret{}, errors.New("invalid encrypted A2A push job")
	}
	plaintext, err := h.a2aPushVault.aead.Open(nil, payload.Nonce, payload.Ciphertext, []byte(job.OwnerKey+"\x00"+job.ResourceID))
	if err != nil {
		return a2aPushJobSecret{}, errors.New("decrypt A2A push job")
	}
	var secret a2aPushJobSecret
	if json.Unmarshal(plaintext, &secret) != nil || secret.CredentialID == "" || !validLifecycleToken(secret.RequestID) || secret.Config.validate() != nil {
		return a2aPushJobSecret{}, errors.New("invalid A2A push job secret")
	}
	return secret, nil
}

func (h Handler) ProcessA2APushNotifications(ctx context.Context) (int, error) {
	if h.a2aPushJobs == nil || h.a2aPushVault == nil || h.a2aTasks == nil {
		return 0, errors.New("A2A push runtime is unavailable")
	}
	jobs, err := h.a2aPushJobs.ClaimAsyncJobs(ctx, a2aPushJobKind, 1, 30*time.Second)
	if err != nil || len(jobs) == 0 {
		return len(jobs), err
	}
	job := jobs[0]
	if err := h.processA2APushNotification(ctx, job); err != nil {
		retryErr := h.a2aPushJobs.RetryAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration, a2aPushRetry(job.Attempts))
		return 1, errors.Join(err, retryErr)
	}
	return 1, h.a2aPushJobs.CompleteAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration)
}

func (h Handler) processA2APushNotification(ctx context.Context, job asyncstate.Job) error {
	if h.a2aHTTPClient == nil {
		return errors.New("A2A push HTTP client is unavailable")
	}
	secret, err := h.decodeA2APushJob(job)
	if err != nil {
		return err
	}
	stored, err := h.a2aTasks.GetA2ATask(ctx, job.OwnerKey, job.EndpointID, job.ResourceID)
	if errors.Is(err, a2astate.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	task, responseID, err := decodeA2AStoredTask(stored.Payload)
	if err != nil {
		return err
	}
	if responseID != "" && a2aTaskPending(task.Status.State) {
		req := modules.RequestContext{CredentialID: secret.CredentialID, UserID: secret.UserID, TeamID: secret.TeamID, OrganizationID: secret.OrganizationID, RequestID: secret.RequestID}
		_, task, err = h.reconcileA2ABackgroundTask(ctx, req, stored, task, responseID)
		if err != nil {
			return err
		}
	}
	if !a2aTaskTerminal(task.Status.State) {
		return errA2ATaskStatusUnavailable
	}
	body, err := json.Marshal(map[string]any{"task": task})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, secret.Config.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/a2a+json")
	if secret.Config.Token != "" {
		request.Header.Set("X-A2A-Notification-Token", secret.Config.Token)
	}
	if secret.Config.Authentication != nil {
		request.Header.Set("Authorization", secret.Config.Authentication.Schemes[0]+" "+secret.Config.Authentication.Credentials)
	}
	response, err := h.a2aHTTPClient.Do(request)
	if err != nil {
		return errors.New("A2A push delivery failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("A2A push endpoint returned status %d", response.StatusCode)
	}
	return nil
}

func a2aPushRetry(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return min(time.Second<<min(attempt-1, 10), time.Hour)
}

func RunA2APushWorker(ctx context.Context, handler Handler) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := handler.ProcessA2APushNotifications(ctx); err != nil && ctx.Err() == nil {
			log.Printf("A2A push notification processing failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
