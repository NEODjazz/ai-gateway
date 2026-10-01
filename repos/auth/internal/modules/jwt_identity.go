package modules

type JWTIdentity struct {
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
	Audience     string `json:"audience"`
	PolicyDigest string `json:"policy_digest"`
}
