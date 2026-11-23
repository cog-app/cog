package certs

import (
	"os"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// sample are plausible credentials, by variable, for every provider.
var sample = map[string]string{
	"OVH_ENDPOINT":             "ovh-eu",
	"DNSUPDATE_NAMESERVER":     "127.0.0.1:53",
	"DNSUPDATE_TSIG_ALGORITHM": "hmac-sha256.",
	"DNSUPDATE_TSIG_SECRET":    "c2VjcmV0",
	"HTTPREQ_ENDPOINT":         "https://dns.example.com/",
	"EXEC_PATH":                "/bin/true",
}

// TestDNSProvidersKeepCredentialsOutOfTheEnvironment: every built-in
// provider is made from its credentials alone, and none of them reaches
// the environment the programs Cog starts inherit.
func TestDNSProvidersKeepCredentialsOutOfTheEnvironment(t *testing.T) {
	for _, p := range DNSProviders {
		if p.Name == "namecheap" {
			continue // it asks a web service for this machine's address
		}
		env := map[string]string{}
		for _, v := range p.Vars {
			env[v.Name] = sample[v.Name]
			if env[v.Name] == "" {
				env[v.Name] = "secret-" + v.Name
			}
		}
		_, err := newDNSProvider(p.Name, env)
		require.NoError(t, err, p.Name)
		for k := range env {
			_, set := os.LookupEnv(k)
			require.False(t, set, p.Name+": "+k+" is in the environment")
		}
	}
	_, err := newDNSProvider("duckdns", map[string]string{"DUCKDNS_TOKEN": "x", "AWS_SECRET_ACCESS_KEY": "y"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "AWS_SECRET_ACCESS_KEY is not one of its settings")
}
