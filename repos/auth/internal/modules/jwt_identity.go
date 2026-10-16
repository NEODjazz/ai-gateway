package modules

type JWTIdentity struct {
	ConnectionID string `json:"connection_id,omitempty"`
	SessionHash  string `json:"session_hash,omitempty"`
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
	Audience     string `json:"audience"`
	PolicyDigest string `json:"policy_digest"`
}
