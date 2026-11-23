// Package setup installs Cog from the binary a person downloaded: it asks
// where and how the server should run (in the browser, or at a terminal),
// then copies the binary there, writes the configuration, makes the admin
// account, obtains a certificate and installs a service. Running it again
// finds the install and offers to change it, reset the admin's password or
// remove the service.
//
// Asking and doing are apart. The wizard, which runs as whoever opened it,
// makes a Plan; Apply carries it out, in a process with administrator
// rights when the plan is for a system service. What the server will own
// (its database and certificates) is made by Finish, run as the account
// the server runs as.
//
// Builds with the nosetup tag (images and distribution packages) leave
// this package out.
package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/service"
)

// What a plan does.
const (
	ModeInstall    = "install"     // a new install
	ModeReconfig   = "reconfigure" // change an install
	ModeResetAdmin = "reset-admin" // set the admin's password
	ModeUninstall  = "uninstall"   // remove the service, keep the data
)

// How the server is reached.
const (
	// AccessLocal: from this computer only.
	AccessLocal = "local"
	// AccessLAN: from the local network, over plain HTTP or HTTPS with a
	// self-signed certificate.
	AccessLAN = "lan"
	// AccessTailscale: from the person's devices on their tailnet, through
	// `tailscale serve` (and, with Funnel, from anywhere).
	AccessTailscale = "tailscale"
	// AccessInternet: from anywhere, over HTTPS Cog serves itself, with
	// the router forwarding ports to this computer.
	AccessInternet = "internet"
	// AccessProxy: from anywhere, through a reverse proxy that
	// terminates HTTPS.
	AccessProxy = "proxy"
)

// Plan is everything the wizard asks.
type Plan struct {
	Mode  string        `json:"mode"`
	Scope service.Scope `json:"scope"`
	Dir   string        `json:"dir"`

	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password,omitempty"`

	Access string `json:"access"`
	// Port is the plain HTTP port: the server's only one for local and
	// LAN access, the proxy's upstream, and a local-only side door when
	// Cog serves HTTPS itself.
	Port int `json:"port"`

	// For AccessLAN: serve HTTPS on HTTPSPort with a self-signed
	// certificate, and plain HTTP on this computer only.
	LANHTTPS bool `json:"lan_https,omitempty"`

	// For AccessTailscale.
	TailscaleName string `json:"tailscale_name,omitempty"` // host.tailnet.ts.net
	Funnel        bool   `json:"funnel,omitempty"`
	// PrevTailscale is what the install had set up ("serve", "funnel" or
	// ""), so a change can turn it off.
	PrevTailscale string `json:"prev_tailscale,omitempty"`

	// For AccessInternet (and HTTPSPort for AccessLAN with LANHTTPS).
	Domain      string            `json:"domain,omitempty"`
	Email       string            `json:"email,omitempty"`
	HTTPSPort   int               `json:"https_port,omitempty"`
	Challenge   string            `json:"challenge,omitempty"`
	HTTPPort    int               `json:"http_port,omitempty"` // for http-01 and redirects
	DNSProvider string            `json:"dns_provider,omitempty"`
	DNSEnv      map[string]string `json:"dns_env,omitempty"`
	Staging     bool              `json:"staging,omitempty"`

	// For AccessProxy.
	PublicURL      string `json:"public_url,omitempty"`
	TrustedProxies string `json:"trusted_proxies,omitempty"`

	SSH                 bool `json:"ssh"`
	SSHPort             int  `json:"ssh_port"`
	SSHAccountPasswords bool `json:"ssh_account_passwords"`

	UpdateCheck bool `json:"update_check"`
	UpdateAuto  bool `json:"update_auto"`

	// Firewall opens the ports in the operating system's firewall
	// (system services only: it needs administrator rights).
	Firewall bool `json:"firewall"`

	// Source is the binary to install: the one running setup.
	Source string `json:"source"`

	// dirSet is a folder the person chose, which a change of scope keeps.
	dirSet bool
}

// DefaultPlan is a new install for scope with the defaults filled in.
func DefaultPlan(scope service.Scope, source string) (Plan, error) {
	dir, err := DefaultDir(scope)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{
		Mode:        ModeInstall,
		Scope:       scope,
		Dir:         dir,
		AdminUser:   "admin",
		Access:      AccessLocal,
		Port:        8580,
		HTTPSPort:   8443,
		HTTPPort:    8080,
		Challenge:   certs.TLSALPN01,
		SSH:         true,
		SSHPort:     8522,
		UpdateCheck: true,
		Source:      source,
	}
	if scope == service.System {
		// A server, not behind a router that translates ports.
		p.HTTPSPort, p.HTTPPort = 443, 80
		p.Firewall = true
	}
	return p, nil
}

// FromConfig is a plan to change the install whose configuration is cfg.
func FromConfig(cfg config.Config, source string) (Plan, error) {
	scope := service.Scope(cfg.Install.Service)
	if scope == "" {
		scope = service.User
	}
	p, err := DefaultPlan(scope, source)
	if err != nil {
		return p, err
	}
	p.Mode = ModeReconfig
	p.Dir = cfg.Install.Dir
	if p.Dir == "" && cfg.File != "" {
		p.Dir = filepath.Dir(cfg.File)
	}
	p.AdminUser = ""
	host, port := splitAddr(cfg.Addr)
	if port != 0 {
		p.Port = port
	}
	p.PrevTailscale = cfg.Install.Tailscale
	if _, port := splitAddr(cfg.TLS.Addr); port != 0 {
		p.HTTPSPort = port
	}
	switch {
	case cfg.Install.Tailscale != "":
		p.Access = AccessTailscale
		p.Funnel = cfg.Install.Tailscale == TailscaleFunnel
		p.TailscaleName = strings.TrimPrefix(cfg.BaseURL, "https://")
	case cfg.TLS.Mode == certs.SelfSigned:
		p.Access = AccessLAN
		p.LANHTTPS = true
	case cfg.TLS.Enabled():
		p.Access = AccessInternet
		if len(cfg.TLS.Domains) > 0 {
			p.Domain = cfg.TLS.Domains[0]
		}
		p.Email = cfg.TLS.Email
		if _, port := splitAddr(cfg.TLS.HTTPAddr); port != 0 {
			p.HTTPPort = port
		}
		if cfg.TLS.Challenge != "" {
			p.Challenge = cfg.TLS.Challenge
		}
		p.DNSProvider = cfg.TLS.DNSProvider
		p.DNSEnv = cfg.TLS.DNSEnv
		p.Staging = cfg.TLS.CA == "staging"
	case cfg.TrustedProxies != "":
		p.Access = AccessProxy
		p.PublicURL = cfg.BaseURL
		p.TrustedProxies = cfg.TrustedProxies
	case host == "127.0.0.1" || host == "localhost" || host == "::1":
		p.Access = AccessLocal
	default:
		p.Access = AccessLAN
	}
	p.SSH = cfg.SSHEnabled
	if _, port := splitAddr(cfg.SSHAddr); port != 0 {
		p.SSHPort = port
	}
	p.SSHAccountPasswords = !cfg.SSHNoAccountPasswords
	p.UpdateCheck = cfg.Update.Check
	p.UpdateAuto = cfg.Update.Auto
	return p, nil
}

func splitAddr(addr string) (string, int) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return host, 0
	}
	return host, n
}

// Layout is where an install keeps things, under its directory.
type Layout struct {
	Dir    string
	Bin    string // the binary
	Config string
	DB     string
	Data   string
}

// Layout is p's install layout.
func (p Plan) Layout() Layout {
	exe := "cog"
	if runtime.GOOS == "windows" {
		exe = "cog.exe"
	}
	return Layout{
		Dir:    p.Dir,
		Bin:    filepath.Join(p.Dir, "bin", exe),
		Config: filepath.Join(p.Dir, config.FileName),
		DB:     filepath.Join(p.Dir, "system.db"),
		Data:   filepath.Join(p.Dir, "data"),
	}
}

// Account is the account the server runs as: its own for a system
// service, else the user's.
func (p Plan) Account() string {
	if p.Scope == service.System {
		return service.DefaultAccount
	}
	return ""
}

// Service is the service p installs.
func (p Plan) Service() service.Spec {
	l := p.Layout()
	return service.Spec{
		Name:        "cog",
		DisplayName: "Cog",
		Description: "Cog file storage and sync server",
		Scope:       p.Scope,
		Exe:         l.Bin,
		Args:        []string{"serve", "--config", l.Config},
		Dir:         l.Dir,
		Account:     p.Account(),
	}
}

var domainPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,62}$`)

// Problem is something wrong with a plan, for the field it is about.
type Problem struct {
	Field   string
	Message string
}

func (p Problem) Error() string { return p.Message }

func problem(field, format string, args ...any) error {
	return Problem{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Validate reports the first thing wrong with p.
func (p *Plan) Validate() error {
	probs, err := p.Problems()
	if err != nil {
		return err
	}
	if len(probs) > 0 {
		return probs[0]
	}
	return nil
}

// dirProblem says why Cog cannot be installed in dir, or "". Cog fills the
// folder, and a system install gives all of it to the server's account,
// so it must be new, empty, or an earlier install's (it has cog.toml): not
// a folder of other things, such as /srv or a home folder.
func dirProblem(dir string) string {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(entries) == 0 {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("Cannot read %s: %v.", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileName)); err == nil {
		return ""
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("Cannot read %s: %v.", dir, err)
	}
	return fmt.Sprintf("%s already holds other files. Choose a new or empty folder, or the folder of an earlier Cog install.", dir)
}

// Problems are everything wrong with p, in the order the wizard asks
// about them, so a page can show the ones about its own fields. err is a
// plan no wizard makes (an unknown mode).
func (p *Plan) Problems() (probs []Problem, err error) {
	p.Domain = strings.ToLower(strings.TrimSpace(p.Domain))
	p.AdminUser = strings.TrimSpace(p.AdminUser)
	add := func(field, format string, args ...any) {
		probs = append(probs, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	port := func(field string, n int) {
		if n < 1 || n > 65535 {
			add(field, "Enter a port between 1 and 65535.")
		}
	}
	switch p.Mode {
	case ModeUninstall:
		return nil, nil
	case ModeResetAdmin:
		p.adminProblems(true, add)
		return probs, nil
	case ModeInstall, ModeReconfig:
	default:
		return nil, fmt.Errorf("unknown setup mode %q", p.Mode)
	}
	if p.Scope != service.System && p.Scope != service.User {
		add("scope", "Choose how Cog runs.")
	}
	if !filepath.IsAbs(p.Dir) {
		add("dir", "The folder must be a full path, such as %s.", examplePath())
	} else if msg := dirProblem(p.Dir); msg != "" {
		add("dir", "%s", msg)
	}
	port("port", p.Port)
	switch p.Access {
	case AccessLocal:
	case AccessLAN:
		if p.LANHTTPS {
			port("https_port", p.HTTPSPort)
		}
	case AccessTailscale:
		if p.TailscaleName == "" {
			add("tailscale", "Tailscale is not connected on this computer.")
		}
	case AccessInternet:
		if !domainPattern.MatchString(p.Domain) {
			add("domain", "Enter the domain name that points at this computer, such as cloud.example.com.")
		}
		if p.Email != "" {
			if _, err := mail.ParseAddress(p.Email); err != nil {
				add("email", "Enter an email address, or leave it empty.")
			}
		}
		port("https_port", p.HTTPSPort)
		switch p.Challenge {
		case certs.TLSALPN01:
		case certs.HTTP01:
			port("http_port", p.HTTPPort)
		case certs.DNS01:
			if prov, ok := certs.FindDNSProvider(p.DNSProvider); !ok {
				add("dns_provider", "Choose your DNS host.")
			} else {
				for _, v := range prov.Vars {
					if strings.TrimSpace(p.DNSEnv[v.Name]) == "" {
						add("dns_env."+v.Name, "Enter the %s for %s.", strings.ToLower(v.Label), prov.Label)
					}
				}
			}
		default:
			add("challenge", "Choose how Let's Encrypt checks that the name is yours.")
		}
		if strings.HasPrefix(p.Domain, "*.") && p.Challenge != certs.DNS01 {
			add("domain", "A wildcard name needs the DNS check.")
		}
	case AccessProxy:
		if !strings.HasPrefix(p.PublicURL, "https://") {
			add("public_url", "Enter the https:// address the proxy serves Cog at.")
		}
		for _, cidr := range strings.Split(p.TrustedProxies, ",") {
			if cidr = strings.TrimSpace(cidr); cidr == "" {
				continue
			}
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				add("trusted_proxies", "%q is not an address range such as 192.168.1.10/32.", cidr)
			}
		}
	default:
		add("access", "Choose where you will use Cog from.")
	}
	if p.SSH {
		port("ssh_port", p.SSHPort)
	}
	if p.Firewall && p.Scope != service.System {
		add("firewall", "Opening the firewall needs a system service.")
	}
	p.adminProblems(p.Mode == ModeInstall, add)
	ports := map[int]string{}
	for _, pt := range p.ports() {
		if other, ok := ports[pt.Number]; ok && pt.Number != 0 {
			add(pt.Field, "%s and %s cannot both use port %d.", other, pt.Name, pt.Number)
		}
		ports[pt.Number] = pt.Name
	}
	return probs, nil
}

func examplePath() string {
	if runtime.GOOS == "windows" {
		return `D:\Cog`
	}
	return "/srv/cog"
}

func (p *Plan) adminProblems(needPassword bool, add func(field, format string, args ...any)) {
	if p.AdminUser == "" {
		if p.Mode != ModeReconfig {
			add("admin_user", "Enter a name for the admin account.")
		}
		return // reconfigure: keep the accounts as they are
	}
	switch {
	case p.AdminPassword == "" && !needPassword && p.Mode == ModeReconfig:
		add("admin_password", "Enter a password for %s, or leave the admin name empty.", p.AdminUser)
	case len(p.AdminPassword) < 8 && (needPassword || p.AdminPassword != ""):
		add("admin_password", "Use at least 8 characters for the admin password.")
	}
}

// validateAdmin reports the first thing wrong with the admin account.
func (p *Plan) validateAdmin(needPassword bool) error {
	var first error
	p.adminProblems(needPassword, func(field, format string, args ...any) {
		if first == nil {
			first = problem(field, format, args...)
		}
	})
	return first
}

// planPort is a port the server listens on and the plan field it comes
// from.
type planPort struct {
	service.Port
	Field string
}

func (p Plan) ports() []planPort {
	port := func(n int, field, name string) planPort {
		return planPort{service.Port{Number: n, Proto: "tcp", Name: name}, field}
	}
	ports := []planPort{port(p.Port, "port", "web (HTTP)")}
	if p.Access == AccessInternet || (p.Access == AccessLAN && p.LANHTTPS) {
		ports = append(ports, port(p.HTTPSPort, "https_port", "web (HTTPS)"))
	}
	if p.Access == AccessInternet && p.Challenge == certs.HTTP01 {
		ports = append(ports, port(p.HTTPPort, "http_port", "HTTP redirect"))
	}
	if p.SSH {
		ports = append(ports, port(p.SSHPort, "ssh_port", "SSH"))
	}
	return ports
}

// Ports are the ports the server listens on, by what they are for.
func (p Plan) Ports() []service.Port {
	var ports []service.Port
	for _, pt := range p.ports() {
		ports = append(ports, pt.Port)
	}
	return ports
}

// PlainLocalOnly reports whether the plain HTTP port answers only on this
// computer: with nothing else to reach, and when HTTPS (Cog's own, or
// Tailscale's in front of it) is how everyone else connects.
func (p Plan) PlainLocalOnly() bool {
	switch p.Access {
	case AccessLocal, AccessInternet, AccessTailscale:
		return true
	case AccessLAN:
		return p.LANHTTPS
	}
	return false
}

// Forward is a rule to add to a router: its public port to this computer's.
type Forward struct {
	Public int
	Local  int
	Name   string
}

// Forwards are the router rules p needs, for internet access through the
// router (Tailscale and a proxy need none).
func (p Plan) Forwards() []Forward {
	if p.Access != AccessInternet {
		return nil
	}
	// 443 even with the DNS check: it is where people reach Cog.
	f := []Forward{{Public: 443, Local: p.HTTPSPort, Name: "HTTPS"}}
	if p.Challenge == certs.HTTP01 {
		f = append(f, Forward{Public: 80, Local: p.HTTPPort, Name: "HTTP (certificate check and redirect)"})
	}
	if p.SSH {
		f = append(f, Forward{Public: p.SSHPort, Local: p.SSHPort, Name: "SSH (optional)"})
	}
	return f
}

// Config is the configuration p makes from base, the install's current
// configuration (or config.Defaults for a new one): settings the wizard
// does not ask about are kept.
func (p Plan) Config(base config.Config) config.Config {
	l := p.Layout()
	cfg := base
	cfg.File = l.Config
	cfg.DBPath = l.DB
	cfg.DataDir = l.Data
	cfg.Install = config.Install{Service: string(p.Scope), Dir: p.Dir, Account: p.Account()}
	cfg.TLS = config.TLS{}
	cfg.TrustedProxies = ""
	// Plain HTTP stays, on this computer only when everyone else uses
	// HTTPS, for the command line and anything set up to use it before.
	if p.PlainLocalOnly() {
		cfg.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port))
	} else {
		cfg.Addr = ":" + strconv.Itoa(p.Port)
	}
	switch p.Access {
	case AccessLocal:
		cfg.BaseURL = "http://localhost:" + strconv.Itoa(p.Port)
	case AccessLAN:
		if !p.LANHTTPS {
			cfg.BaseURL = "http://" + net.JoinHostPort(LANAddress(), strconv.Itoa(p.Port))
			break
		}
		lan := LANAddress()
		cfg.BaseURL = "https://" + lan
		if p.HTTPSPort != 443 {
			cfg.BaseURL = "https://" + net.JoinHostPort(lan, strconv.Itoa(p.HTTPSPort))
		}
		cfg.TLS = config.TLS{
			Mode:    certs.SelfSigned,
			Addr:    ":" + strconv.Itoa(p.HTTPSPort),
			Domains: SelfSignedNames(lan),
		}
	case AccessTailscale:
		cfg.BaseURL = "https://" + p.TailscaleName
		// tailscale serve connects from this computer and says who the
		// visitor is in X-Forwarded-For.
		cfg.TrustedProxies = "127.0.0.1/32,::1/128"
		cfg.Install.Tailscale = TailscaleServe
		if p.Funnel {
			cfg.Install.Tailscale = TailscaleFunnel
		}
	case AccessInternet:
		cfg.BaseURL = "https://" + strings.TrimPrefix(p.Domain, "*.")
		cfg.TLS = config.TLS{
			Mode:        "acme",
			Addr:        ":" + strconv.Itoa(p.HTTPSPort),
			Domains:     []string{p.Domain},
			Email:       p.Email,
			Challenge:   p.Challenge,
			DNSProvider: p.DNSProvider,
			DNSEnv:      p.DNSEnv,
		}
		if p.Challenge == certs.HTTP01 {
			cfg.TLS.HTTPAddr = ":" + strconv.Itoa(p.HTTPPort)
		}
		if p.Challenge != certs.DNS01 {
			cfg.TLS.DNSProvider, cfg.TLS.DNSEnv = "", nil
		}
		if p.Staging {
			cfg.TLS.CA = "staging"
		}
	case AccessProxy:
		cfg.BaseURL = strings.TrimSuffix(p.PublicURL, "/")
		cfg.TrustedProxies = p.TrustedProxies
	}
	cfg.SSHEnabled = p.SSH
	cfg.SSHAddr = ":" + strconv.Itoa(p.SSHPort)
	cfg.SSHNoAccountPasswords = !p.SSHAccountPasswords
	cfg.Update.Check = p.UpdateCheck
	cfg.Update.Auto = p.UpdateAuto
	return cfg
}

// LANAddress is this computer's address on the local network: the one a
// router forwards to. It is the address of the route to the internet, so
// it is right on a computer with more than one network. A variable so
// that a test's pages do not depend on the machine's network.
var LANAddress = lanAddress

func lanAddress() string {
	// UDP "connects" without sending anything; it only picks the route.
	c, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		// No route anywhere: there is no network to be reached on.
		return "localhost"
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return "localhost"
}

// SelfSignedNames are the names a self-signed certificate covers: this
// computer's names and its address on the network.
func SelfSignedNames(lan string) []string {
	names := []string{"localhost", "127.0.0.1"}
	if host, err := Hostname(); err == nil && host != "" {
		host = strings.ToLower(strings.TrimSuffix(host, ".local"))
		names = append([]string{host, host + ".local"}, names...)
	}
	if lan != "" && lan != "localhost" {
		names = append([]string{lan}, names...)
	}
	return names
}

// Hostname is this computer's name. A variable so that a test's pages do
// not depend on the machine.
var Hostname = os.Hostname

// errElevation is an apply that needs administrator rights it could not
// get.
var errElevation = errors.New("administrator rights are needed to install a system service")
