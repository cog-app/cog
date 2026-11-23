package certs

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
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
	// Vars are the credentials it needs, by the name of the variable lego
	// would read them from (how cog.toml's [tls.dns_env] names them).
	Vars []DNSVar
	New  func(creds) (challenge.Provider, error)
}

// creds are a provider's credentials by variable name.
type creds map[string]string

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
			func(v creds) (challenge.Provider, error) {
				c := cloudflare.NewDefaultConfig()
				c.AuthToken = v[cloudflare.EnvDNSAPIToken]
				return cloudflare.NewDNSProviderConfig(c)
			}},
		{"duckdns", "Duck DNS", []DNSVar{{duckdns.EnvToken, "Token", true}},
			func(v creds) (challenge.Provider, error) {
				c := duckdns.NewDefaultConfig()
				c.Token = v[duckdns.EnvToken]
				return duckdns.NewDNSProviderConfig(c)
			}},
		{"desec", "deSEC", []DNSVar{{desec.EnvToken, "Token", true}},
			func(v creds) (challenge.Provider, error) {
				c := desec.NewDefaultConfig()
				c.Token = v[desec.EnvToken]
				return desec.NewDNSProviderConfig(c)
			}},
		{"digitalocean", "DigitalOcean", []DNSVar{{digitalocean.EnvAuthToken, "API token", true}},
			func(v creds) (challenge.Provider, error) {
				c := digitalocean.NewDefaultConfig()
				c.AuthToken = v[digitalocean.EnvAuthToken]
				return digitalocean.NewDNSProviderConfig(c)
			}},
		{"hetzner", "Hetzner", []DNSVar{{hetzner.EnvAPIToken, "API token", true}},
			func(v creds) (challenge.Provider, error) {
				c := hetzner.NewDefaultConfig()
				c.APIToken = v[hetzner.EnvAPIToken]
				return hetzner.NewDNSProviderConfig(c)
			}},
		{"porkbun", "Porkbun", []DNSVar{{porkbun.EnvAPIKey, "API key", false}, {porkbun.EnvSecretAPIKey, "Secret API key", true}},
			func(v creds) (challenge.Provider, error) {
				c := porkbun.NewDefaultConfig()
				c.APIKey, c.SecretAPIKey = v[porkbun.EnvAPIKey], v[porkbun.EnvSecretAPIKey]
				return porkbun.NewDNSProviderConfig(c)
			}},
		{"gandiv5", "Gandi", []DNSVar{{gandiv5.EnvPersonalAccessToken, "Personal access token", true}},
			func(v creds) (challenge.Provider, error) {
				c := gandiv5.NewDefaultConfig()
				c.PersonalAccessToken = v[gandiv5.EnvPersonalAccessToken]
				return gandiv5.NewDNSProviderConfig(c)
			}},
		{"namecheap", "Namecheap", []DNSVar{{namecheap.EnvAPIUser, "API user", false}, {namecheap.EnvAPIKey, "API key", true}},
			func(v creds) (challenge.Provider, error) {
				c := namecheap.NewDefaultConfig()
				c.APIUser, c.APIKey = v[namecheap.EnvAPIUser], v[namecheap.EnvAPIKey]
				return namecheap.NewDNSProviderConfig(c)
			}},
		{"ovh", "OVH", []DNSVar{{ovh.EnvEndpoint, "Endpoint (ovh-eu, ovh-ca, ...)", false}, {ovh.EnvApplicationKey, "Application key", false}, {ovh.EnvApplicationSecret, "Application secret", true}, {ovh.EnvConsumerKey, "Consumer key", true}},
			func(v creds) (challenge.Provider, error) {
				c := ovh.NewDefaultConfig()
				c.APIEndpoint = cmp.Or(v[ovh.EnvEndpoint], "ovh-eu")
				c.ApplicationKey, c.ApplicationSecret, c.ConsumerKey = v[ovh.EnvApplicationKey], v[ovh.EnvApplicationSecret], v[ovh.EnvConsumerKey]
				return ovh.NewDNSProviderConfig(c)
			}},
		{"rfc2136", "RFC 2136 (BIND, Knot, PowerDNS)", []DNSVar{{rfc2136.EnvNameserver, "Name server (host:port)", false}, {rfc2136.EnvTSIGKey, "TSIG key name", false}, {rfc2136.EnvTSIGSecret, "TSIG secret", true}, {rfc2136.EnvTSIGAlgorithm, "TSIG algorithm (hmac-sha256.)", false}},
			func(v creds) (challenge.Provider, error) {
				c := rfc2136.NewDefaultConfig()
				c.Nameserver = v[rfc2136.EnvNameserver]
				c.TSIGKey, c.TSIGSecret = v[rfc2136.EnvTSIGKey], v[rfc2136.EnvTSIGSecret]
				c.TSIGAlgorithm = cmp.Or(v[rfc2136.EnvTSIGAlgorithm], c.TSIGAlgorithm)
				return rfc2136.NewDNSProviderConfig(c)
			}},
		{"httpreq", "HTTP request (lego httpreq)", []DNSVar{{httpreq.EnvEndpoint, "Endpoint URL", false}, {httpreq.EnvUsername, "Username", false}, {httpreq.EnvPassword, "Password", true}},
			func(v creds) (challenge.Provider, error) {
				c := httpreq.NewDefaultConfig()
				u, err := url.Parse(v[httpreq.EnvEndpoint])
				if err != nil {
					return nil, fmt.Errorf("endpoint: %w", err)
				}
				c.Endpoint, c.Username, c.Password = u, v[httpreq.EnvUsername], v[httpreq.EnvPassword]
				return httpreq.NewDNSProviderConfig(c)
			}},
		{"exec", "Program (lego exec)", []DNSVar{{exec.EnvPath, "Program path", false}},
			func(v creds) (challenge.Provider, error) {
				c := exec.NewDefaultConfig()
				c.Program = v[exec.EnvPath]
				return exec.NewDNSProviderConfig(c)
			}},
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

// newDNSProvider makes the provider called name with the credentials in
// env. They go to the provider's configuration, never into the process's
// environment, which every program Cog starts (plugins, media tools)
// would inherit.
func newDNSProvider(name string, env map[string]string) (challenge.Provider, error) {
	p, ok := FindDNSProvider(name)
	if !ok {
		return nil, fmt.Errorf("unknown DNS provider %q", name)
	}
	for k := range env {
		if !slices.ContainsFunc(p.Vars, func(v DNSVar) bool { return v.Name == k }) {
			return nil, fmt.Errorf("DNS provider %s: %s is not one of its settings", p.Label, k)
		}
	}
	prov, err := p.New(env)
	if err != nil {
		return nil, fmt.Errorf("DNS provider %s: %w", p.Label, err)
	}
	return prov, nil
}
