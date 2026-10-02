package modules

import (
	"context"
	"slices"
)

func (m AuthModule) SSOManager() *SSOManager { return m.sso }

func (m AuthModule) SaveSSODraft(ctx context.Context, input SSODraftInput) (SSOSettingsView, error) {
	api, err := m.currentJWTModule(ctx)
	if err != nil {
		return SSOSettingsView{}, err
	}
	if api.jwtConfig.Issuer == input.Issuer && api.jwtConfig.Audience == input.ClientID {
		return SSOSettingsView{}, ErrSSOConfiguration
	}
	return m.sso.SaveDraft(ctx, input)
}

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

func (m AuthModule) VerifySSOTest(ctx context.Context, profileID, ticket, token string, nonce ...string) error {
	state, revision, err := m.sso.Load(ctx)
	if err != nil {
		return err
	}
	if state.Draft == nil || state.Draft.ID != profileID || !validSSOTicket(state.Attempt, ticket, m.sso.now()) {
		return ErrSSOConfiguration
	}
	if len(nonce) < 1 || len(nonce) > 2 {
		return ErrSSOConfiguration
	}
	login := SSOBrowserLogin{ProfileID: profileID, Token: token, Nonce: nonce[0]}
	if len(nonce) == 2 {
		login.AccessToken = nonce[1]
	}
	req, err := m.verifySSOIdentity(ctx, state.Draft, login)
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
		candidate.jwtConfig.Audience = state.Draft.ClientID
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
		if state.Active != nil {
			profile := *state.Active
			key, err := ssoRandom()
			if err != nil {
				return SSOSettingsView{}, err
			}
			profile.SessionKey = key
			state.Active = &profile
		}
		state.Draft, state.Attempt = nil, nil
	default:
		return SSOSettingsView{}, ErrSSOConfiguration
	}
	if err := m.sso.save(ctx, revision, state); err != nil {
		return SSOSettingsView{}, err
	}
	return m.sso.View(ctx)
}
