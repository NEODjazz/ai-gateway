package modules

import (
	"context"
	"slices"
)

func (m AuthModule) SSOManager() *SSOManager { return m.sso }

type SSOTestProfile struct {
	Profile *SSOProfile `json:"profile"`
	Attempt *SSOAttempt `json:"attempt"`
}

func (m *SSOManager) TestProfile(ctx context.Context) (SSOTestProfile, error) {
	state, _, err := m.Load(ctx)
	if err != nil {
		return SSOTestProfile{}, err
	}
	if state.Draft == nil || state.Attempt == nil || state.Attempt.Status != "running" || state.Attempt.ExpiresAt <= m.now().Unix() {
		return SSOTestProfile{}, ErrSSOConfiguration
	}
	return SSOTestProfile{Profile: state.Draft, Attempt: state.Attempt}, nil
}

func (m AuthModule) VerifySSOTest(ctx context.Context, profileID, ticket, token string) error {
	state, revision, err := m.sso.Load(ctx)
	if err != nil {
		return err
	}
	if state.Draft == nil || state.Draft.ID != profileID || !validSSOTicket(state.Attempt, ticket, m.sso.now()) {
		return ErrSSOConfiguration
	}
	verifier, err := newJWTVerifier(state.Draft.jwtConfig())
	if err != nil {
		return ErrSSOConfiguration
	}
	candidate := m
	candidate.jwtConfig, candidate.jwtVerifier, candidate.sso = state.Draft.jwtConfig(), verifier, nil
	req := RequestContext{APIKey: token}
	err = candidate.authorizeJWTConfigured(ctx, &req)
	if err != nil || req.UserID != state.Attempt.ActorID || !slices.Contains(req.Roles, "admin") || req.JWTIdentity == nil {
		state.Attempt.Status = "failed"
		if saveErr := m.sso.save(ctx, revision, state); saveErr != nil {
			return saveErr
		}
		return ErrUnauthorized
	}
	state.Attempt.Status = "passed"
	state.Attempt.UserID, state.Attempt.CredentialID = req.UserID, req.CredentialID
	state.Attempt.Roles, state.Attempt.Identity = req.Roles, req.JWTIdentity
	return m.sso.save(ctx, revision, state)
}

func (m AuthModule) ChangeSSO(ctx context.Context, action string, expectedRevision int64, actor string) (SSOSettingsView, error) {
	state, revision, err := m.sso.Load(ctx)
	if err != nil {
		return SSOSettingsView{}, err
	}
	if revision != expectedRevision {
		return SSOSettingsView{}, ErrSSOConflict
	}
	switch action {
	case "activate":
		attempt := state.Attempt
		if state.Draft == nil || attempt == nil || attempt.Status != "passed" || attempt.ExpiresAt <= m.sso.now().Unix() || attempt.ActorID != actor || attempt.UserID != actor || !slices.Contains(attempt.Roles, "admin") {
			return SSOSettingsView{}, ErrSSOConfiguration
		}
		candidate := m
		candidate.sso, candidate.jwtConfig = nil, state.Draft.jwtConfig()
		req := RequestContext{UserID: attempt.UserID, CredentialID: attempt.CredentialID, Roles: attempt.Roles, JWTIdentity: attempt.Identity}
		if err := candidate.ReauthorizeJWTPrincipal(ctx, &req); err != nil {
			return SSOSettingsView{}, err
		}
		state.Previous, state.Active, state.Draft = state.Active, state.Draft, nil
		state.CanRollback, state.Attempt = true, nil
	case "disable":
		if state.Active == nil {
			return SSOSettingsView{}, ErrSSOConfiguration
		}
		previous := *state.Active
		state.Previous = &previous
		state.Active.Enabled = false
		state.CanRollback = true
	case "rollback":
		if !state.CanRollback {
			return SSOSettingsView{}, ErrSSOConfiguration
		}
		state.Active, state.Previous = state.Previous, state.Active
		state.Draft, state.Attempt = nil, nil
	default:
		return SSOSettingsView{}, ErrSSOConfiguration
	}
	if err := m.sso.save(ctx, revision, state); err != nil {
		return SSOSettingsView{}, err
	}
	return m.sso.View(ctx)
}
