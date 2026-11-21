package gallery

func (p *Plugin) OwnersFirst(usernames []string) ([]string, map[string][]string) {
	return p.ownersFirst(usernames)
}
