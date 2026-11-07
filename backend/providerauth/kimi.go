package providerauth

import "github.com/v0lka/c0wrk/core/version"

const (
	KimiRegionGlobal     = "global"
	KimiRegionMainlandCN = "mainland_cn"
	kimiProviderID       = "kimi"
	kimiClientID         = "17e5f671-d194-4dfb-9706-5516cb48c098"
)

// Kimi returns the global Kimi for Coding subscription profile.
func Kimi() Profile {
	return KimiForRegion(KimiRegionGlobal)
}

// KimiForRegion returns one of the two compile-time subscription profiles.
func KimiForRegion(region string) Profile {
	if region != KimiRegionMainlandCN {
		region = KimiRegionGlobal
	}
	profile := Profile{
		ProviderID: kimiProviderID,
		ClientID:   kimiClientID,
		DeviceFlow: true,
		Region:     region,
		APIHeaders: map[string]string{
			"User-Agent":        "c0wrk/" + version.Version,
			"anthropic-version": "2023-06-01",
		},
	}
	if region == KimiRegionMainlandCN {
		profile.Issuer = "https://auth.kimi.com"
		profile.APIBaseURL = "https://api.kimi.com/coding/v1"
		return profile
	}
	profile.Issuer = "https://auth.kimi.ai"
	profile.APIBaseURL = "https://api.kimi.ai/coding/v1"
	return profile
}
