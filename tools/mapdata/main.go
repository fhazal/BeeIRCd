// Command mapdata turns a Natural Earth admin-0 countries GeoJSON file
// (public domain, https://www.naturalearthdata.com) into the small map file
// the Hive Panel draws its network map from:
//
//	{"v":1,"src":"...","scale":100,"rings":[[x0,y0,dx1,dy1,...],...]}
//
// Each ring is one country outline (outer rings only: the panel fills the
// land and strokes the outlines, so a hole would only be another country),
// as longitude/latitude times scale, the first point whole and the rest
// as differences. Rings are simplified (Douglas-Peucker, -tol degrees) and
// tiny islands dropped (-min square degrees).
//
//	go run ./tools/mapdata -in ne_110m_admin_0_countries.geojson -out internal/hivepanel/ui/map/world-110.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

type geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

type pt [2]float64

func main() {
	in := flag.String("in", "", "Natural Earth admin-0 countries GeoJSON")
	out := flag.String("out", "", "map file to write")
	src := flag.String("src", "Natural Earth admin-0 countries", "source note written into the file")
	tol := flag.Float64("tol", 0.05, "simplify: largest error in degrees")
	min := flag.Float64("min", 0.05, "drop rings smaller than this (square degrees of their box)")
	scale := flag.Float64("scale", 100, "points per degree")
	flag.Parse()
	if *in == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		fail(err)
	}
	var fc struct {
		Features []struct {
			Geometry geometry `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		fail(err)
	}
	var rings [][]pt
	for _, f := range fc.Features {
		var polys [][][]pt
		switch f.Geometry.Type {
		case "Polygon":
			var p [][]pt
			if err := json.Unmarshal(f.Geometry.Coordinates, &p); err != nil {
				fail(err)
			}
			polys = [][][]pt{p}
		case "MultiPolygon":
			if err := json.Unmarshal(f.Geometry.Coordinates, &polys); err != nil {
				fail(err)
			}
		default:
			continue
		}
		for _, p := range polys {
			if len(p) == 0 {
				continue
			}
			r := p[0]
			if boxArea(r) < *min {
				continue
			}
			r = simplify(r, *tol)
			if len(r) >= 4 {
				rings = append(rings, r)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"v":1,"src":%q,"scale":%s,"rings":[`, *src, strconv.FormatFloat(*scale, 'f', -1, 64))
	pointsOut := 0
	for i, r := range rings {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		var px, py int
		for j, p := range r {
			x, y := int(math.Round(p[0]**scale)), int(math.Round(p[1]**scale))
			if j > 0 {
				b.WriteByte(',')
				fmt.Fprintf(&b, "%d,%d", x-px, y-py)
			} else {
				fmt.Fprintf(&b, "%d,%d", x, y)
			}
			px, py = x, y
			pointsOut++
		}
		b.WriteByte(']')
	}
	b.WriteString("]}\n")
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("%s: %d rings, %d points, %d bytes\n", *out, len(rings), pointsOut, b.Len())
}

func boxArea(r []pt) float64 {
	if len(r) == 0 {
		return 0
	}
	x0, x1, y0, y1 := r[0][0], r[0][0], r[0][1], r[0][1]
	for _, p := range r {
		x0, x1, y0, y1 = math.Min(x0, p[0]), math.Max(x1, p[0]), math.Min(y0, p[1]), math.Max(y1, p[1])
	}
	return (x1 - x0) * (y1 - y0)
}

// simplify is Douglas-Peucker on one ring, keeping its first and last point.
func simplify(r []pt, tol float64) []pt {
	if len(r) < 3 || tol <= 0 {
		return r
	}
	keep := make([]bool, len(r))
	keep[0], keep[len(r)-1] = true, true
	var walk func(a, b int)
	walk = func(a, b int) {
		best, at := 0.0, -1
		for i := a + 1; i < b; i++ {
			if d := lineDist(r[i], r[a], r[b]); d > best {
				best, at = d, i
			}
		}
		if at >= 0 && best > tol {
			keep[at] = true
			walk(a, at)
			walk(at, b)
		}
	}
	walk(0, len(r)-1)
	out := make([]pt, 0, len(r)/2)
	for i, k := range keep {
		if k {
			out = append(out, r[i])
		}
	}
	return out
}

func lineDist(p, a, b pt) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if l := math.Hypot(dx, dy); l > 1e-12 {
		return math.Abs(dy*p[0]-dx*p[1]+b[0]*a[1]-b[1]*a[0]) / l
	}
	return math.Hypot(p[0]-a[0], p[1]-a[1])
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mapdata:", err)
	os.Exit(1)
}
