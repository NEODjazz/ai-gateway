package modules

import "strings"

func Auth(required bool, url string) Module {
	if url != "" {
		return NewRemoteModule("auth", required, endpoint(url, "/authorize"))
	}
	return NewAuthModule(required)
}

func Anonymizer(required bool, url string) Module {
	if url != "" {
		return NewRemoteModule("anonymizer", required, endpoint(url, "/anonymize"))
	}
	return NewAnonymizerModule(required, AnonymizerRulesFromEnv()...)
}

func Billing(required bool, url string) Module {
	if url != "" {
		return NewRemotePostResponseModule("billing", required, endpoint(url, "/usage"))
	}
	return NewBillingModule(required)
}

func DLP(required bool, url string) Module {
	if url == "" {
		return NewProviderRemoteModule("dlp", required, "")
	}
	return NewProviderRemoteModule("dlp", required, endpoint(url, "/scan"))
}

func AV(required bool, url string) Module {
	if url == "" {
		return NewProviderRemoteModule("av", required, "")
	}
	return NewProviderRemoteModule("av", required, endpoint(url, "/scan"))
}

func endpoint(baseURL string, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}
