package recipes

import "io"

// SaveImage stores an image as a user's recipe image named name, so a test
// can give a recipe a photo without fetching one.
func (p *Plugin) SaveImage(username, name string, r io.Reader) error {
	store, err := p.ctx.UserStorage(username)
	if err != nil {
		return err
	}
	_, err = store.Put(imageName(name), r)
	return err
}
