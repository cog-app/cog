package gallery

import "fmt"

// FaceOverlay holds CSS-ready coordinates for rendering a face box on an image.
type FaceOverlay struct {
	PersonID        *int64
	Name            string
	Left, Top       string // CSS percentage values
	Width, Height   string
	Visible         bool
	IsCurrentPerson bool
}

// TransformFaceForCover transforms normalized face coordinates for display on a
// square-cropped thumbnail (object-fit:cover). It returns CSS percentage-based
// coordinates clipped to the visible region.
func TransformFaceForCover(f Face, imageW, imageH int, currentPersonID int64) FaceOverlay {
	overlay := FaceOverlay{
		PersonID: f.PersonID,
		Name:     f.Name,
	}
	if f.PersonID != nil && *f.PersonID == currentPersonID {
		overlay.IsCurrentPerson = true
	}

	if imageW <= 0 || imageH <= 0 {
		return overlay
	}

	// Face coords are normalized 0..1 within the original image.
	fx, fy, fw, fh := f.X, f.Y, f.W, f.H

	// For square crop (object-fit:cover), the browser centers the image and
	// crops the longer dimension. We need to transform face coords from
	// image-space to container-space (the visible square).
	var cx, cy, cw, ch float64

	if imageW > imageH {
		// Landscape: image is wider than tall. Vertically fits, horizontally cropped.
		ratio := float64(imageW) / float64(imageH)
		// Map x: container_x = face_x * ratio - (ratio - 1) / 2
		cx = fx*ratio - (ratio-1)/2
		cy = fy
		cw = fw * ratio
		ch = fh
	} else if imageH > imageW {
		// Portrait: image is taller than wide. Horizontally fits, vertically cropped.
		ratio := float64(imageH) / float64(imageW)
		cx = fx
		cy = fy*ratio - (ratio-1)/2
		cw = fw
		ch = fh * ratio
	} else {
		// Square: no transformation needed.
		cx, cy, cw, ch = fx, fy, fw, fh
	}

	// Clamp to visible region [0, 1].
	x1, y1 := cx, cy
	x2, y2 := cx+cw, cy+ch

	if x2 <= 0 || y2 <= 0 || x1 >= 1 || y1 >= 1 {
		// Entirely outside visible area.
		return overlay
	}

	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 > 1 {
		x2 = 1
	}
	if y2 > 1 {
		y2 = 1
	}

	overlay.Left = fmt.Sprintf("%.2f%%", x1*100)
	overlay.Top = fmt.Sprintf("%.2f%%", y1*100)
	overlay.Width = fmt.Sprintf("%.2f%%", (x2-x1)*100)
	overlay.Height = fmt.Sprintf("%.2f%%", (y2-y1)*100)
	overlay.Visible = true
	return overlay
}
