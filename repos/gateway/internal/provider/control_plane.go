package provider

import (
	"context"
	"errors"
	"time"
)

// ControlPlaneSnapshot is the durable management state. Credential material is
// already encrypted by the Router before it crosses the store boundary.
type ControlPlaneSnapshot struct {
	Revision    int64                         `json:"revision"`
	Providers   []ManagedProvider             `json:"providers"`
	Credentials []EncryptedCredentialSnapshot `json:"credentials"`
	Deployments []ModelDeployment             `json:"deployments"`
	ModelGroups []ModelGroup                  `json:"model_groups"`
}

type EncryptedCredentialSnapshot struct {
	Credential Credential `json:"credential"`
	Nonce      []byte     `json:"nonce"`
	Ciphertext []byte     `json:"ciphertext"`
}

type ControlPlaneStore interface {
	Load(context.Context) (ControlPlaneSnapshot, bool, error)
	Save(context.Context, int64, ControlPlaneSnapshot) (int64, error)
	Revision(context.Context) (int64, error)
}

var ErrControlPlaneConflict = errors.New("control plane revision conflict")

const DefaultControlPlaneRefreshInterval = time.Second
