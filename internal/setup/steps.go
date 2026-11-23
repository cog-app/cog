package setup

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/service"
)

// The guided wizard asks one question a page, in this order, skipping the
// pages a plan does not need (no domain to ask for when Cog stays at
// home). Each page owns the plan fields it asks about: a problem with one
// of them keeps the person on that page, and problems with fields later
// pages own wait for those pages.
//
// The quick setup (/quick) asks everything on one page, for people who
// know what they want.

// Step is a page of the guided wizard.
type Step struct {
	ID       string
	Question string
	// Fields are the plan fields this page sets; a name ending in "."
	// covers every field with that prefix.
	Fields []string
	Show   func(Plan) bool
}

func always(Plan) bool { return true }

func accessIs(a string) func(Plan) bool {
	return func(p Plan) bool { return p.Access == a }
}

// Steps are the guided wizard's pages.
var Steps = []Step{
	{ID: "run", Question: "How should Cog run?", Fields: []string{"scope"},
		Show: func(p Plan) bool { return p.Mode == ModeInstall }},
	{ID: "storage", Question: "Where do you want to keep your files?", Fields: []string{"dir"}, Show: always},
	{ID: "access", Question: "Where will you use Cog from?", Fields: []string{"access", "port"}, Show: always},
	{ID: "network", Question: "Encrypt connections on your network?", Fields: []string{"lan_https", "https_port"},
		Show: accessIs(AccessLAN)},
	{ID: "tailscale", Question: "Who on Tailscale can reach Cog?", Fields: []string{"tailscale", "funnel"},
		Show: accessIs(AccessTailscale)},
	{ID: "domain", Question: "What is Cog's address on the internet?", Fields: []string{"domain", "email"},
		Show: accessIs(AccessInternet)},
	{ID: "certificate", Question: "How should Let's Encrypt check the name is yours?",
		Fields: []string{"challenge", "http_port", "https_port", "dns_provider", "dns_env.", "staging"},
		Show:   accessIs(AccessInternet)},
	{ID: "proxy", Question: "Where does your reverse proxy serve Cog?", Fields: []string{"public_url", "trusted_proxies"},
		Show: accessIs(AccessProxy)},
	{ID: "ssh", Question: "Do you want SFTP and SSH?", Fields: []string{"ssh", "ssh_port", "ssh_account_passwords"}, Show: always},
	{ID: "updates", Question: "How should Cog stay up to date?", Fields: []string{"update_check", "update_auto"}, Show: always},
	{ID: "admin", Question: "Who manages Cog?", Fields: []string{"admin_user", "admin_password", "admin_password_confirm"}, Show: always},
}

// Owns reports whether field is asked on this page.
func (s Step) Owns(field string) bool {
	for _, f := range s.Fields {
		if f == field || (strings.HasSuffix(f, ".") && strings.HasPrefix(field, f)) {
			return true
		}
	}
	return false
}

// VisibleSteps are the pages p needs, in order.
func VisibleSteps(p Plan) []Step {
	var out []Step
	for _, s := range Steps {
		if s.Show(p) {
			out = append(out, s)
		}
	}
	return out
}

// findStep is the page id among p's pages and its index, -1 if p does
// not need it.
func findStep(p Plan, id string) (Step, int) {
	steps := VisibleSteps(p)
	i := slices.IndexFunc(steps, func(s Step) bool { return s.ID == id })
	if i < 0 {
		return Step{}, -1
	}
	return steps[i], i
}

// Problems is a page's problems, for its fields to show.
type Problems []Problem

// For is the message for field, "" if it has none.
func (ps Problems) For(field string) string {
	for _, p := range ps {
		if p.Field == field {
			return p.Message
		}
	}
	return ""
}

// First is the first problem's message.
func (ps Problems) First() string {
	if len(ps) == 0 {
		return ""
	}
	return ps[0].Message
}

// readStep sets the fields the page id asks about from its form. The
// problems it returns are ones only a page can find (passwords that
// differ, Tailscale not running).
func readStep(ctx context.Context, id string, f url.Values, p *Plan) Problems {
	var probs Problems
	switch id {
	case "run":
		p.Scope = service.Scope(f.Get("scope"))
		// A server gets the ports and firewall a server needs; a home
		// computer the unprivileged ports its router translates to.
		def, err := DefaultPlan(p.Scope, p.Source)
		if err == nil {
			p.HTTPSPort, p.HTTPPort, p.Firewall = def.HTTPSPort, def.HTTPPort, def.Firewall
			if !dirChanged(*p) {
				p.Dir = def.Dir
			}
		}
	case "storage":
		p.Dir = strings.TrimSpace(f.Get("dir"))
		p.dirSet = true
	case "access":
		p.Access = f.Get("access")
		if f.Has("port") {
			p.Port = atoi(f.Get("port"))
		}
		if p.Access == AccessTailscale {
			st := CheckTailscale(ctx)
			if msg := st.Problem(); msg != "" {
				probs = append(probs, Problem{Field: "access", Message: msg})
			} else {
				p.TailscaleName = st.Name
			}
		}
		if p.Access == AccessInternet || (p.Access == AccessTailscale && p.Funnel) {
			// Reachable from anywhere: SSH takes keys only unless the
			// person says otherwise on the SSH page.
			p.SSHAccountPasswords = false
		}
	case "network":
		p.LANHTTPS = f.Get("lan_https") == "1"
		if f.Has("https_port") {
			p.HTTPSPort = atoi(f.Get("https_port"))
		}
	case "tailscale":
		p.Funnel = f.Get("funnel") == "1"
		if p.Funnel {
			p.SSHAccountPasswords = false
		}
	case "domain":
		p.Domain = f.Get("domain")
		p.Email = strings.TrimSpace(f.Get("email"))
	case "certificate":
		p.Challenge = f.Get("challenge")
		if f.Has("https_port") {
			p.HTTPSPort = atoi(f.Get("https_port"))
		}
		if f.Has("http_port") {
			p.HTTPPort = atoi(f.Get("http_port"))
		}
		readDNS(f, p)
		p.Staging = f.Get("staging") == "1"
	case "proxy":
		p.PublicURL = strings.TrimSpace(f.Get("public_url"))
		p.TrustedProxies = strings.TrimSpace(f.Get("trusted_proxies"))
	case "ssh":
		p.SSH = f.Get("ssh") == "1"
		if f.Has("ssh_port") {
			p.SSHPort = atoi(f.Get("ssh_port"))
		}
		p.SSHAccountPasswords = f.Get("ssh_account_passwords") == "1"
	case "updates":
		p.UpdateCheck = f.Get("update_check") == "1"
		p.UpdateAuto = p.UpdateCheck && f.Get("update_auto") == "1"
	case "admin":
		p.AdminUser = f.Get("admin_user")
		p.AdminPassword = f.Get("admin_password")
		if p.AdminPassword != f.Get("admin_password_confirm") {
			probs = append(probs, Problem{Field: "admin_password_confirm", Message: "The passwords do not match."})
		}
	}
	return probs
}

// dirChanged reports whether the person chose the folder rather than
// taking the default for the scope.
func dirChanged(p Plan) bool { return p.dirSet }

func readDNS(f url.Values, p *Plan) {
	p.DNSProvider = f.Get("dns_provider")
	p.DNSEnv = map[string]string{}
	if prov, ok := certs.FindDNSProvider(p.DNSProvider); ok {
		for _, v := range prov.Vars {
			p.DNSEnv[v.Name] = strings.TrimSpace(f.Get("dns_env." + v.Name))
		}
	}
}

// stepProblems are p's problems that page s owns, after extra.
func stepProblems(p *Plan, s Step, extra Problems) (Problems, error) {
	all, err := p.Problems()
	if err != nil {
		return nil, err
	}
	probs := extra
	for _, pr := range all {
		if s.Owns(pr.Field) && probs.For(pr.Field) == "" {
			probs = append(probs, pr)
		}
	}
	return probs, nil
}

// firstStepWithProblems is the first page of p's with something to fix,
// -1 if none.
func firstStepWithProblems(p Plan) (Step, bool, error) {
	all, err := p.Problems()
	if err != nil {
		return Step{}, false, err
	}
	for _, s := range VisibleSteps(p) {
		for _, pr := range all {
			if s.Owns(pr.Field) {
				return s, true, nil
			}
		}
	}
	return Step{}, false, nil
}

// stepView is what a guided page shows.
type stepView struct {
	Plan      Plan
	Step      Step
	Index     int // among the plan's pages
	Total     int
	Problems  Problems
	Back      string // the previous page, "" on the first
	Reviewing bool   // the review has been seen: saving returns to it
	LAN       string // this computer's address on the network
	Admin     bool   // setup has administrator rights
	Tailscale TailscaleStatus
}
