package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/service"
)

// CLI is setup at a terminal, asking the wizard's questions in turn.
type CLI struct {
	Exe string
	In  *os.File
	Out io.Writer

	r   *bufio.Reader
	eof bool // input ended: every question since took its default
}

// errInputEnded is input that ended before setup was confirmed: a
// script that ran out of answers must not install on defaults.
var errInputEnded = errors.New("input ended before setup was confirmed; nothing was changed")

// Run asks, shows the plan, and carries it out once confirmed.
func (c *CLI) Run(ctx context.Context) error {
	c.r = bufio.NewReader(c.In)
	installs, err := Find(c.Exe)
	if err != nil {
		return err
	}
	p, err := c.choose(installs)
	if err != nil {
		return err
	}
	switch p.Mode {
	case ModeInstall, ModeReconfig:
		if err := c.ask(&p); err != nil {
			return err
		}
		c.review(ctx, p)
	case ModeResetAdmin:
		if err := c.askAdmin(&p, true); err != nil {
			return err
		}
	}
	if c.eof {
		return errInputEnded
	}
	if !c.yes("Go ahead?", true) || c.eof {
		return errors.New("nothing was changed")
	}
	res, err := Run(ctx, p, c.Out, false)
	for _, w := range res.Warnings {
		fmt.Fprintf(c.Out, "\nWarning: %s\n", w)
	}
	if err != nil {
		return err
	}
	if res.Fingerprint != "" {
		fmt.Fprintf(c.Out, "\nThe certificate's SHA-256 fingerprint, to compare with what your browser shows:\n  %s\n", res.Fingerprint)
	}
	if res.URL != "" {
		fmt.Fprintf(c.Out, "\nCog is running: open %s\n", res.URL)
	}
	return nil
}

func (c *CLI) choose(installs []Install) (Plan, error) {
	if len(installs) == 0 {
		scope := service.User
		fmt.Fprintln(c.Out, "Set up Cog.")
		fmt.Fprintln(c.Out, "  1) On this computer, for me: starts when you sign in, no administrator rights needed.")
		fmt.Fprintln(c.Out, "  2) As a system service: starts with the computer, runs as an account of its own.")
		if c.choice("How should Cog run?", 1, 2) == 2 {
			scope = service.System
		}
		return DefaultPlan(scope, c.Exe)
	}
	fmt.Fprintln(c.Out, "Cog is already set up:")
	for i, in := range installs {
		state := "not running"
		if in.Status.Running {
			state = "running"
		}
		fmt.Fprintf(c.Out, "  %d) %s in %s (%s)\n", i+1, installTitle(in), in.Dir, state)
	}
	fmt.Fprintf(c.Out, "  %d) Set up a new, separate install\n", len(installs)+1)
	n := c.choice("Which?", 1, len(installs)+1)
	if n == len(installs)+1 {
		installs = nil
		return c.choose(nil)
	}
	in := installs[n-1]
	p, err := in.Plan(c.Exe)
	if err != nil {
		return p, err
	}
	fmt.Fprintln(c.Out, "  1) Change its settings")
	fmt.Fprintln(c.Out, "  2) Reset an admin's password")
	fmt.Fprintln(c.Out, "  3) Remove its service (files and data stay)")
	switch c.choice("What should setup do?", 1, 3) {
	case 2:
		p.Mode, p.AdminUser = ModeResetAdmin, "admin"
	case 3:
		p.Mode = ModeUninstall
	}
	return p, nil
}

func (c *CLI) ask(p *Plan) error {
	p.Dir = c.text("Folder for Cog's program, settings and data", p.Dir)
	if err := c.askAdmin(p, p.Mode == ModeInstall); err != nil {
		return err
	}

	fmt.Fprintln(c.Out, "\nWhere will you use Cog from?")
	accesses := []string{AccessLocal, AccessLAN, AccessTailscale, AccessInternet, AccessProxy}
	fmt.Fprintln(c.Out, "  1) Only on this computer")
	fmt.Fprintf(c.Out, "  2) At home, on my network (%s)\n", LANAddress())
	fmt.Fprintln(c.Out, "  3) Anywhere, on my devices with Tailscale")
	fmt.Fprintln(c.Out, "  4) Anywhere, including people I share with: HTTPS from Let's Encrypt, your router forwarding ports here")
	fmt.Fprintln(c.Out, "  5) Through a reverse proxy I already run")
	def := 1
	for i, a := range accesses {
		if a == p.Access {
			def = i + 1
		}
	}
	for {
		p.Access = accesses[c.choice("Choose", def, len(accesses))-1]
		if p.Access != AccessTailscale {
			break
		}
		st := CheckTailscale(context.Background())
		if msg := st.Problem(); msg == "" || c.eof {
			p.TailscaleName = st.Name
			break
		} else {
			fmt.Fprintln(c.Out, msg)
		}
	}
	p.Port = c.number("Cog's HTTP port on this computer", p.Port)

	switch p.Access {
	case AccessLAN:
		p.LANHTTPS = c.yes("Encrypt with HTTPS, using a certificate Cog makes (browsers warn once per device)?", p.LANHTTPS)
		if p.LANHTTPS {
			p.HTTPSPort = c.number("HTTPS port", p.HTTPSPort)
		}
	case AccessTailscale:
		fmt.Fprintf(c.Out, "Cog will be at https://%s for your devices on Tailscale.\n", p.TailscaleName)
		p.Funnel = c.yes("Also let people without Tailscale in, with Tailscale Funnel (for share links)?", p.Funnel)
	case AccessInternet:
		p.Domain = c.text("Domain name pointing at your public address (e.g. cloud.example.com)", p.Domain)
		p.Email = c.text("Email for Let's Encrypt (optional)", p.Email)
		p.HTTPSPort = c.number("HTTPS port on this computer (your router forwards 443 to it)", p.HTTPSPort)
		fmt.Fprintln(c.Out, "How should Let's Encrypt check the name is yours?")
		fmt.Fprintln(c.Out, "  1) Through the HTTPS port (only 443 forwarded)")
		fmt.Fprintln(c.Out, "  2) Through port 80 (80 forwarded too)")
		fmt.Fprintln(c.Out, "  3) Through my DNS host's API (no ports needed; wildcards)")
		challenges := []string{certs.TLSALPN01, certs.HTTP01, certs.DNS01}
		def := 1
		for i, ch := range challenges {
			if ch == p.Challenge {
				def = i + 1
			}
		}
		p.Challenge = challenges[c.choice("Choose", def, 3)-1]
		switch p.Challenge {
		case certs.HTTP01:
			p.HTTPPort = c.number("HTTP port on this computer (your router forwards 80 to it)", p.HTTPPort)
		case certs.DNS01:
			for i, d := range certs.DNSProviders {
				fmt.Fprintf(c.Out, "  %d) %s\n", i+1, d.Label)
			}
			def := 1
			for i, d := range certs.DNSProviders {
				if d.Name == p.DNSProvider {
					def = i + 1
				}
			}
			prov := certs.DNSProviders[c.choice("DNS host", def, len(certs.DNSProviders))-1]
			p.DNSProvider = prov.Name
			env := map[string]string{}
			for _, v := range prov.Vars {
				if v.Secret {
					s, err := c.secret(v.Label)
					if err != nil {
						return err
					}
					env[v.Name] = s
				} else {
					env[v.Name] = c.text(v.Label, p.DNSEnv[v.Name])
				}
			}
			p.DNSEnv = env
		}
		p.Staging = c.yes("Use Let's Encrypt's test service (untrusted certificates, for trying setup out)?", p.Staging)
	case AccessProxy:
		p.PublicURL = c.text("Public https:// address the proxy serves Cog at", p.PublicURL)
		p.TrustedProxies = c.text("The proxy's address range (e.g. 127.0.0.1/32)", p.TrustedProxies)
	}

	p.SSH = c.yes("\nServe SSH and SFTP?", p.SSH)
	if p.SSH {
		p.SSHPort = c.number("SSH port", p.SSHPort)
		public := p.Access == AccessInternet || (p.Access == AccessTailscale && p.Funnel)
		p.SSHAccountPasswords = c.yes("Allow account passwords over SSH (no means keys and app passwords only)?", p.SSHAccountPasswords && !public)
	}
	p.UpdateCheck = c.yes("Check for new versions daily?", p.UpdateCheck)
	if p.UpdateCheck {
		p.UpdateAuto = c.yes("Install them automatically (restarts Cog)?", p.UpdateAuto)
	}
	if p.Scope == service.System && len(p.reachablePorts()) > 0 {
		p.Firewall = c.yes("Open Cog's ports in the firewall?", p.Firewall)
	} else {
		p.Firewall = false
	}
	if c.eof {
		return errInputEnded
	}
	if err := p.Validate(); err != nil {
		fmt.Fprintf(c.Out, "\n%s Let's go through it again.\n\n", err)
		return c.ask(p)
	}
	return nil
}

func (c *CLI) askAdmin(p *Plan, required bool) error {
	if !required {
		fmt.Fprintln(c.Out, "\nLeave the admin name empty to keep the accounts as they are.")
		p.AdminUser = c.text("Admin account (a name that exists has its password reset)", "")
		if p.AdminUser == "" {
			return nil
		}
	} else {
		p.AdminUser = c.text("Admin account name", p.AdminUser)
	}
	for !c.eof {
		pw, err := c.secret("Password for " + p.AdminUser + " (at least 8 characters)")
		if err != nil {
			return err
		}
		again, err := c.secret("Password again")
		if err != nil {
			return err
		}
		if pw != again {
			fmt.Fprintln(c.Out, "The passwords do not match.")
			continue
		}
		p.AdminPassword = pw
		if err := p.validateAdmin(required); err != nil {
			fmt.Fprintln(c.Out, err)
			continue
		}
		return nil
	}
	return errInputEnded
}

func (c *CLI) review(ctx context.Context, p Plan) {
	l := p.Layout()
	fmt.Fprintf(c.Out, "\nSetup will install Cog to %s, with settings in %s.\n", l.Bin, l.Config)
	if p.Scope == service.System {
		fmt.Fprintf(c.Out, "It runs as a system service, as %s.\n", p.Account())
	} else {
		fmt.Fprintln(c.Out, "It runs as you, from when you sign in.")
	}
	switch p.Access {
	case AccessTailscale:
		fmt.Fprintf(c.Out, "\nTailscale will serve https://%s and pass it on to Cog.\n", p.TailscaleName)
		if p.Funnel {
			fmt.Fprintln(c.Out, "With Funnel anyone can reach the sign-in page: turn on two-factor sign-in under Settings > Security.")
		}
	case AccessLAN:
		if p.LANHTTPS {
			fmt.Fprintln(c.Out, "\nBrowsers will warn about the certificate the first time; setup shows its fingerprint to compare.")
		}
	}
	if p.Access == AccessInternet {
		lan := LANAddress()
		fmt.Fprintf(c.Out, "\nAdd these port forwarding rules in your router, to this computer (%s):\n", lan)
		for _, f := range p.Forwards() {
			fmt.Fprintf(c.Out, "  TCP %d -> %s:%d  (%s)\n", f.Public, lan, f.Local, f.Name)
		}
		fmt.Fprintln(c.Out, "and give this computer a fixed address on your network (a DHCP reservation).")
		dns := CheckDNS(ctx, p.Domain)
		switch {
		case dns.Err != nil:
			fmt.Fprintf(c.Out, "\nWarning: %s does not resolve yet (%v).\n", p.Domain, dns.Err)
		case dns.Private:
			fmt.Fprintf(c.Out, "\nWarning: %s points at %s, inside a private network; Let's Encrypt cannot reach it.\n", p.Domain, strings.Join(dns.Addrs, ", "))
		default:
			fmt.Fprintf(c.Out, "\n%s points at %s; that should be your public address.\n", p.Domain, strings.Join(dns.Addrs, ", "))
		}
		fmt.Fprintln(c.Out, "Anyone can reach the sign-in page: turn on two-factor sign-in under Settings > Security.")
	}
	fmt.Fprintln(c.Out)
}

func (c *CLI) line() string {
	s, err := c.r.ReadString('\n')
	if err != nil {
		c.eof = true
	}
	return strings.TrimSpace(s)
}

func (c *CLI) text(q, def string) string {
	if def != "" {
		fmt.Fprintf(c.Out, "%s [%s]: ", q, def)
	} else {
		fmt.Fprintf(c.Out, "%s: ", q)
	}
	if s := c.line(); s != "" {
		return s
	}
	return def
}

func (c *CLI) number(q string, def int) int {
	for {
		s := c.text(q, strconv.Itoa(def))
		if n, err := strconv.Atoi(s); err == nil || c.eof {
			return n
		}
		fmt.Fprintln(c.Out, "Enter a number.")
	}
}

func (c *CLI) choice(q string, def, max int) int {
	for {
		n := c.number(q, def)
		if n >= 1 && n <= max {
			return n
		}
		if c.eof {
			return def
		}
		fmt.Fprintf(c.Out, "Enter 1 to %d.\n", max)
	}
}

func (c *CLI) yes(q string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(c.Out, "%s [%s]: ", q, hint)
		switch strings.ToLower(c.line()) {
		case "":
			return def && !c.eof
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

func (c *CLI) secret(q string) (string, error) {
	fmt.Fprintf(c.Out, "%s: ", q)
	if !term.IsTerminal(int(c.In.Fd())) {
		return c.line(), nil
	}
	b, err := term.ReadPassword(int(c.In.Fd()))
	fmt.Fprintln(c.Out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}
