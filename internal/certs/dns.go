package certs

import (
	"fmt"
	"os"
	"sort"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/desec"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/duckdns"
	"github.com/go-acme/lego/v4/providers/dns/exec"
	"github.com/go-acme/lego/v4/providers/dns/gandiv5"
	"github.com/go-acme/lego/v4/providers/dns/hetzner"
	"github.com/go-acme/lego/v4/providers/dns/httpreq"
	"github.com/go-acme/lego/v4/providers/dns/namecheap"
	"github.com/go-acme/lego/v4/providers/dns/ovh"
	"github.com/go-acme/lego/v4/providers/dns/porkbun"
	"github.com/go-acme/lego/v4/providers/dns/rfc2136"
)

// DNSProvider is a DNS host Cog can prove a name with (dns-01), for a
// wildcard certificate or a server that cannot take connections on 80 or
// 443. Only these are built in: every lego provider would add the cloud
// SDKs of AWS, Azure and Google to every binary.
type DNSProvider struct {
	Name  string
	Label string
	// Vars are the credentials it needs, by the variable lego reads.
	Vars []DNSVar
	New  func() (challenge.Provider, error)
}

// DNSVar is one credential a DNS provider needs.
type DNSVar struct {
	Name   string
	Label  string
	Secret bool
}

// DNSProviders lists the built-in DNS providers by label.
var DNSProviders = func() []DNSProvider {
	ps := []DNSProvider{
		{"cloudflare", "Cloudflare", []DNSVar{{cloudflare.EnvDNSAPIToken, "API token (Zone:DNS:Edit)", true}},
			func() (challenge.Provider, error) { return cloudflare.NewDNSProvider() }},
		{"duckdns", "Duck DNS", []DNSVar{{duckdns.EnvToken, "Token", true}},
			func() (challenge.Provider, error) { return duckdns.NewDNSProvider() }},
		{"desec", "deSEC", []DNSVar{{desec.EnvToken, "Token", true}},
			func() (challenge.Provider, error) { return desec.NewDNSProvider() }},
		{"digitalocean", "DigitalOcean", []DNSVar{{digitalocean.EnvAuthToken, "API token", true}},
			func() (challenge.Provider, error) { return digitalocean.NewDNSProvider() }},
		{"hetzner", "Hetzner", []DNSVar{{hetzner.EnvAPIToken, "API token", true}},
			func() (challenge.Provider, error) { return hetzner.NewDNSProvider() }},
		{"porkbun", "Porkbun", []DNSVar{{porkbun.EnvAPIKey, "API key", false}, {porkbun.EnvSecretAPIKey, "Secret API key", true}},
			func() (challenge.Provider, error) { return porkbun.NewDNSProvider() }},
		{"gandiv5", "Gandi", []DNSVar{{gandiv5.EnvPersonalAccessToken, "Personal access token", true}},
			func() (challenge.Provider, error) { return gandiv5.NewDNSProvider() }},
		{"namecheap", "Namecheap", []DNSVar{{namecheap.EnvAPIUser, "API user", false}, {namecheap.EnvAPIKey, "API key", true}},
			func() (challenge.Provider, error) { return namecheap.NewDNSProvider() }},
		{"ovh", "OVH", []DNSVar{{ovh.EnvEndpoint, "Endpoint (ovh-eu, ovh-ca, ...)", false}, {ovh.EnvApplicationKey, "Application key", false}, {ovh.EnvApplicationSecret, "Application secret", true}, {ovh.EnvConsumerKey, "Consumer key", true}},
			func() (challenge.Provider, error) { return ovh.NewDNSProvider() }},
		{"rfc2136", "RFC 2136 (BIND, Knot, PowerDNS)", []DNSVar{{rfc2136.EnvNameserver, "Name server (host:port)", false}, {rfc2136.EnvTSIGKey, "TSIG key name", false}, {rfc2136.EnvTSIGSecret, "TSIG secret", true}, {rfc2136.EnvTSIGAlgorithm, "TSIG algorithm (hmac-sha256.)", false}},
			func() (challenge.Provider, error) { return rfc2136.NewDNSProvider() }},
		{"httpreq", "HTTP request (lego httpreq)", []DNSVar{{httpreq.EnvEndpoint, "Endpoint URL", false}, {httpreq.EnvUsername, "Username", false}, {httpreq.EnvPassword, "Password", true}},
			func() (challenge.Provider, error) { return httpreq.NewDNSProvider() }},
		{"exec", "Program (lego exec)", []DNSVar{{exec.EnvPath, "Program path", false}},
			func() (challenge.Provider, error) { return exec.NewDNSProvider() }},
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Label < ps[j].Label })
	return ps
}()

// FindDNSProvider returns the built-in DNS provider called name.
func FindDNSProvider(name string) (DNSProvider, bool) {
	for _, p := range DNSProviders {
		if p.Name == name {
			return p, true
		}
	}
	return DNSProvider{}, false
}

// newDNSProvider makes the provider called name with the credentials in env.
// lego providers read their credentials from the environment, so they are
// set there; one server has one provider.
func newDNSProvider(name string, env map[string]string) (challenge.Provider, error) {
	p, ok := FindDNSProvider(name)
	if !ok {
		return nil, fmt.Errorf("unknown DNS provider %q", name)
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			return nil, fmt.Errorf("set %s: %w", k, err)
		}
	}
	prov, err := p.New()
	if err != nil {
		return nil, fmt.Errorf("DNS provider %s: %w", p.Label, err)
	}
	return prov, nil
}
