package gallery

import "testing"

func TestTransformFaceForCover(t *testing.T) {
	pid := int64(1)
	baseFace := Face{
		ID: 1, MediaID: 1, PersonID: &pid, Name: "Alice",
		X: 0.4, Y: 0.3, W: 0.2, H: 0.2,
	}

	t.Run("square image", func(t *testing.T) {
		o := TransformFaceForCover(baseFace, 100, 100, 1)
		if !o.Visible {
			t.Fatal("expected visible")
		}
		if !o.IsCurrentPerson {
			t.Fatal("expected current person")
		}
	})

	t.Run("landscape image", func(t *testing.T) {
		o := TransformFaceForCover(baseFace, 200, 100, 1)
		if !o.Visible {
			t.Fatal("expected visible")
		}
	})

	t.Run("portrait image", func(t *testing.T) {
		o := TransformFaceForCover(baseFace, 100, 200, 1)
		if !o.Visible {
			t.Fatal("expected visible")
		}
	})

	t.Run("face cropped out in landscape", func(t *testing.T) {
		// Face at far right of a very wide image — will be cropped
		f := Face{X: 0.95, Y: 0.5, W: 0.05, H: 0.1}
		o := TransformFaceForCover(f, 400, 100, 0)
		if o.Visible {
			t.Fatal("expected face to be outside visible area")
		}
	})

	t.Run("face cropped out in portrait", func(t *testing.T) {
		// Face at bottom of very tall image — will be cropped
		f := Face{X: 0.5, Y: 0.95, W: 0.1, H: 0.05}
		o := TransformFaceForCover(f, 100, 400, 0)
		if o.Visible {
			t.Fatal("expected face to be outside visible area")
		}
	})

	t.Run("zero dimensions", func(t *testing.T) {
		o := TransformFaceForCover(baseFace, 0, 0, 1)
		if o.Visible {
			t.Fatal("expected not visible for zero dimensions")
		}
	})

	t.Run("not current person", func(t *testing.T) {
		o := TransformFaceForCover(baseFace, 100, 100, 99)
		if o.IsCurrentPerson {
			t.Fatal("expected not current person")
		}
	})

	t.Run("extreme landscape ratio", func(t *testing.T) {
		// Center face should still be visible
		f := Face{X: 0.5, Y: 0.5, W: 0.05, H: 0.1}
		o := TransformFaceForCover(f, 1000, 100, 0)
		// Center of a 10:1 image: container_x = 0.5*10 - 4.5 = 0.5, should be visible
		if !o.Visible {
			t.Fatal("expected center face to remain visible")
		}
	})
}
