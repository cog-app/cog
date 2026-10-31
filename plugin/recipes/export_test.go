package recipes

// ImagesDir exposes where a user's recipe images live, so a test can give a
// recipe a photo without fetching one.
func (p *Plugin) ImagesDir(username string) string {
	return p.imagesDir(username)
}
