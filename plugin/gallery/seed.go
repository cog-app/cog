package gallery

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
)

// seedPhotos defines the procedural images to generate.
var seedPhotos = []struct {
	path    string
	pattern string // "gradient", "checker", "circles", "stripes", "plasma", "diamonds", "waves", "spiral"
	w, h    int
	hue     float64 // base hue 0-360
}{
	// Vacation folder — warm tones
	{"Photos/Vacation/sunset_beach.jpg", "gradient", 800, 600, 20},
	{"Photos/Vacation/ocean_waves.jpg", "waves", 800, 600, 200},
	{"Photos/Vacation/palm_trees.jpg", "stripes", 600, 800, 120},
	{"Photos/Vacation/resort_pool.jpg", "circles", 800, 600, 180},
	{"Photos/Vacation/tropical_fish.jpg", "plasma", 800, 600, 30},

	// Nature folder — greens and blues
	{"Photos/Nature/mountain_vista.jpg", "gradient", 1024, 768, 210},
	{"Photos/Nature/forest_trail.jpg", "stripes", 768, 1024, 130},
	{"Photos/Nature/wildflowers.jpg", "circles", 800, 600, 60},
	{"Photos/Nature/river_bend.jpg", "waves", 1024, 768, 190},
	{"Photos/Nature/autumn_leaves.jpg", "diamonds", 800, 600, 30},
	{"Photos/Nature/starry_sky.jpg", "plasma", 1024, 768, 250},

	// City folder — cool tones
	{"Photos/City/downtown_skyline.jpg", "gradient", 1024, 576, 220},
	{"Photos/City/neon_signs.jpg", "plasma", 800, 600, 300},
	{"Photos/City/street_market.jpg", "checker", 800, 600, 40},
	{"Photos/City/bridge_at_night.jpg", "circles", 1024, 576, 260},

	// Family folder — warm tones
	{"Photos/Family/birthday_party.jpg", "circles", 800, 600, 350},
	{"Photos/Family/garden_picnic.jpg", "checker", 800, 600, 90},
	{"Photos/Family/holiday_dinner.jpg", "gradient", 800, 600, 15},
	{"Photos/Family/kids_playing.jpg", "stripes", 600, 800, 50},

	// Misc — root level photos (simulating auto-upload mess)
	{"InstantUpload/IMG_20240715_143022.jpg", "spiral", 640, 480, 180},
	{"InstantUpload/IMG_20240715_143055.jpg", "waves", 640, 480, 90},
	{"InstantUpload/IMG_20240716_091533.jpg", "diamonds", 640, 480, 270},
	{"InstantUpload/IMG_20240718_184201.jpg", "gradient", 640, 480, 330},
	{"InstantUpload/IMG_20240720_110845.jpg", "plasma", 640, 480, 60},
}

// Seed creates sample gallery images for a user.
func (p *Plugin) Seed(username string) error {
	fp, err := p.filesPlugin()
	if err != nil {
		return fmt.Errorf("seed gallery: %w", err)
	}
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed gallery: %w", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&count); err != nil {
		return fmt.Errorf("seed gallery: %w", err)
	}
	if count > 0 {
		return nil
	}

	for _, photo := range seedPhotos {
		img := generateImage(photo.pattern, photo.w, photo.h, photo.hue)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
			p.log.Error("seed encode", "path", photo.path, "err", err)
			continue
		}

		if _, err := fp.Upload(username, photo.path, &buf); err != nil {
			p.log.Error("seed upload", "path", photo.path, "err", err)
			continue
		}
	}

	p.log.Info("seeded gallery, running initial scan", "count", len(seedPhotos))

	// Run the scanner immediately so media appears in the gallery right away.
	if _, err := p.scanUserFully(context.Background(), username); err != nil {
		p.log.Error("seed scan", "err", err)
	}

	return nil
}

// generateImage creates a procedural image with the given pattern.
func generateImage(pattern string, w, h int, hue float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	switch pattern {
	case "gradient":
		drawGradient(img, hue)
	case "checker":
		drawChecker(img, hue)
	case "circles":
		drawCircles(img, hue)
	case "stripes":
		drawStripes(img, hue)
	case "plasma":
		drawPlasma(img, hue)
	case "diamonds":
		drawDiamonds(img, hue)
	case "waves":
		drawWaves(img, hue)
	case "spiral":
		drawSpiral(img, hue)
	default:
		drawGradient(img, hue)
	}

	return img
}

func drawGradient(img *image.RGBA, hue float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			fx := float64(x-b.Min.X) / float64(b.Dx())
			fy := float64(y-b.Min.Y) / float64(b.Dy())
			h := hue + fx*60
			s := 0.5 + fy*0.4
			l := 0.3 + (1-fy)*0.5
			img.Set(x, y, hslToRGB(h, s, l))
		}
	}
}

func drawChecker(img *image.RGBA, hue float64) {
	b := img.Bounds()
	cellSize := 40
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cx := (x - b.Min.X) / cellSize
			cy := (y - b.Min.Y) / cellSize
			if (cx+cy)%2 == 0 {
				img.Set(x, y, hslToRGB(hue, 0.7, 0.55))
			} else {
				img.Set(x, y, hslToRGB(hue+180, 0.4, 0.75))
			}
		}
	}
}

func drawCircles(img *image.RGBA, hue float64) {
	b := img.Bounds()
	cx := float64(b.Dx()) / 2
	cy := float64(b.Dy()) / 2
	maxR := math.Sqrt(cx*cx + cy*cy)

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x-b.Min.X) - cx
			dy := float64(y-b.Min.Y) - cy
			r := math.Sqrt(dx*dx+dy*dy) / maxR
			ring := math.Sin(r * 20)
			h := hue + ring*40
			l := 0.4 + ring*0.2
			img.Set(x, y, hslToRGB(h, 0.6, l))
		}
	}
}

func drawStripes(img *image.RGBA, hue float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			fx := float64(x-b.Min.X) / float64(b.Dx())
			fy := float64(y-b.Min.Y) / float64(b.Dy())
			v := math.Sin((fx+fy)*30) * 0.5
			img.Set(x, y, hslToRGB(hue+v*30, 0.6, 0.45+v*0.2))
		}
	}
}

func drawPlasma(img *image.RGBA, hue float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			fx := float64(x-b.Min.X) / float64(b.Dx()) * 6
			fy := float64(y-b.Min.Y) / float64(b.Dy()) * 6
			v := math.Sin(fx) + math.Sin(fy) + math.Sin(fx+fy) + math.Sin(math.Sqrt(fx*fx+fy*fy))
			v = v / 4
			h := hue + v*60
			l := 0.4 + v*0.2
			img.Set(x, y, hslToRGB(h, 0.7, l))
		}
	}
}

func drawDiamonds(img *image.RGBA, hue float64) {
	b := img.Bounds()
	size := 60.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			fx := math.Mod(float64(x-b.Min.X), size) - size/2
			fy := math.Mod(float64(y-b.Min.Y), size) - size/2
			d := (math.Abs(fx) + math.Abs(fy)) / size
			h := hue + d*40
			l := 0.35 + d*0.35
			img.Set(x, y, hslToRGB(h, 0.6, l))
		}
	}
}

func drawWaves(img *image.RGBA, hue float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			fx := float64(x-b.Min.X) / float64(b.Dx())
			fy := float64(y-b.Min.Y) / float64(b.Dy())
			v := math.Sin(fx*12+math.Sin(fy*8)*2) * 0.5
			h := hue + v*50
			l := 0.35 + fy*0.35 + v*0.1
			img.Set(x, y, hslToRGB(h, 0.65, l))
		}
	}
}

func drawSpiral(img *image.RGBA, hue float64) {
	b := img.Bounds()
	cx := float64(b.Dx()) / 2
	cy := float64(b.Dy()) / 2
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x-b.Min.X) - cx
			dy := float64(y-b.Min.Y) - cy
			angle := math.Atan2(dy, dx)
			dist := math.Sqrt(dx*dx + dy*dy)
			v := math.Sin(angle*3 + dist*0.05)
			h := hue + v*50 + dist*0.2
			l := 0.35 + v*0.2
			img.Set(x, y, hslToRGB(h, 0.65, l))
		}
	}
}

// hslToRGB converts HSL values to an RGB color.
// h is in degrees (0-360), s and l are in 0-1.
func hslToRGB(h, s, l float64) color.RGBA {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	if l < 0 {
		l = 0
	}
	if l > 1 {
		l = 1
	}

	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2

	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}

	return color.RGBA{
		R: uint8((r + m) * 255),
		G: uint8((g + m) * 255),
		B: uint8((b + m) * 255),
		A: 255,
	}
}
