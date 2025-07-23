package supervisor

import "math"

// Slope calculates the least‑squares slope of the series ys.
// Returns NaN if the slice has fewer than two samples.
func Slope(ys []float64) float64 {
	n := float64(len(ys))
	if n < 2 {
		return math.NaN()
	}

	var sumX, sumY, sumXX, sumXY float64
	for i, y := range ys {
		x := float64(i)
		sumX += x
		sumY += y
		sumXX += x * x
		sumXY += x * y
	}

	num := n*sumXY - sumX*sumY
	den := n*sumXX - sumX*sumX
	return num / den
}

// Trend returns -1 for a decreasing trend, 1 for an increasing trend,
// and 0 when no clear trend exists, using epsilon to ignore tiny slopes.
func Trend(ys []float64, epsilon float64) int {
	b := Slope(ys)
	switch {
	case b > epsilon:
		return 1 // creciente
	case b < -epsilon:
		return -1 // decreciente
	default:
		return 0 // plana
	}
}
