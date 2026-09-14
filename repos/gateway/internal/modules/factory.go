package modules

import (
	"net/url"
	"strings"
)

func Auth(required bool, url string) Module {
	if url != "" {
		return NewRemoteAuthModule(required, endpoint(url, "/authorize"))
	}
	return NewAuthModule(required)
}

func Anonymizer(required bool, url string) Module {
	if url != "" {
		return NewRemoteAnonymizerModule(required, endpoint(url, "/anonymize"))
	}
	return NewAnonymizerModule(required, AnonymizerRulesFromEnv()...)
}

func Billing(required bool, url string) Module {
	if url != "" {
		return NewRemoteBillingModule(required, endpoint(url, "/usage"))
	}
	return NewBillingModule(required)
}

func BillingWithSecret(required bool, url, secret string) Module {
	if url != "" {
		return NewRemoteBillingModuleWithSecret(required, endpoint(url, "/usage"), secret)
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
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return strings.TrimRight(baseURL, "/") + path
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	pathSuffix := strings.TrimRight(path, "/")
	if strings.HasSuffix(basePath, pathSuffix) {
		parsed.Path = basePath
		return parsed.String()
	}

	if basePath == "" {
		parsed.Path = path
		return parsed.String()
	}
	parsed.Path = basePath + path
	return parsed.String()
}
