package localmedia

import (
	"image"
	"math"
)

// sheetLuma reads a tile back from its page as BT.601 full-range luma, the
// plane a viewer's eye weighs most and the one compared tiles share.
func sheetLuma(img *image.RGBA, x, y, width, height int) []byte {
	luma := make([]byte, width*height)
	for row := 0; row < height; row++ {
		at := img.PixOffset(x, y+row)
		for column := 0; column < width; column++ {
			luma[row*width+column] = byte((299*int(img.Pix[at]) + 587*int(img.Pix[at+1]) + 114*int(img.Pix[at+2]) + 500) / 1000)
			at += 4
		}
	}
	return luma
}

// sheetSSIM is the mean structural similarity of two equal luma planes over
// 8x8 windows advancing four pixels, with the usual constants for 8-bit
// samples. Identical planes give exactly 1; a plane smaller than one window
// is compared as a single window.
func sheetSSIM(a, b []byte, width, height int) float64 {
	const window, step = 8, 4
	const c1, c2 = 6.5025, 58.5225 // (0.01*255)^2 and (0.03*255)^2
	windowW, windowH := min(window, width), min(window, height)
	samples := float64(windowW * windowH)
	var total float64
	var windows int
	for top := 0; top+windowH <= height; top += step {
		for left := 0; left+windowW <= width; left += step {
			var sumA, sumB, sumAA, sumBB, sumAB int
			for row := top; row < top+windowH; row++ {
				at := row*width + left
				for column := 0; column < windowW; column++ {
					va, vb := int(a[at+column]), int(b[at+column])
					sumA += va
					sumB += vb
					sumAA += va * va
					sumBB += vb * vb
					sumAB += va * vb
				}
			}
			meanA, meanB := float64(sumA)/samples, float64(sumB)/samples
			varianceA := float64(sumAA)/samples - meanA*meanA
			varianceB := float64(sumBB)/samples - meanB*meanB
			covariance := float64(sumAB)/samples - meanA*meanB
			total += (2*meanA*meanB + c1) * (2*covariance + c2) / ((meanA*meanA + meanB*meanB + c1) * (varianceA + varianceB + c2))
			windows++
		}
	}
	if windows == 0 {
		return 1
	}
	return math.Round(total/float64(windows)*1e6) / 1e6
}
