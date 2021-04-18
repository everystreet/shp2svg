package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	svg "github.com/ajstarks/svgo"
	"github.com/alecthomas/kong"
	"github.com/everystreet/go-shapefile"
	"github.com/everystreet/go-shapefile/shp"
)

func main() {
	var app App
	ctx := kong.Parse(&app)
	ctx.FatalIfErrorf(app.Exec(ctx))
}

// App defines the application cli.
type App struct {
	Shapefiles  []string `kong:"required,name=shapefiles,short=z,help='Path to zipped shapefiles.'"`
	Destination string   `kong:"required,type=path,name=destination,short=d,help='Path to destination SVG.'"`
	Filters     []string `kong:"optional,name=filter,short=f,sep=';',help='Filter expressions.'"`
	Scale       float64  `kong:"optional,default=1,name=scale-factor,short=s,help='Scale factor.'"`
}

// Exec runs the command.
func (a App) Exec(_ *kong.Context) error {
	filters, err := a.parseFilters()
	if err != nil {
		return err
	}

	fields := make(map[string]struct{})
	var shapes shp.Shapes

	for _, path := range a.Shapefiles {
		if err := func() (err error) {
			scanner, closer, err := open(path)
			defer func() {
				if closeErr := closer.Close(); closeErr != nil && err == nil {
					err = closeErr
				}
			}()

			info, err := scanner.Info()
			if err != nil {
				return err
			}

			for _, filter := range filters {
				for _, field := range info.Fields {
					if field.Name() == filter.name {
						fields[field.Name()] = struct{}{}
					}
				}
			}

			if err := scanner.Scan(); err != nil {
				return err
			}

		Record:
			for {
				record := scanner.Record()
				if record == nil {
					break
				}

				for _, field := range record.Fields() {
					for _, filter := range filters {
						if filter.name != field.Name() {
							continue
						}

						for _, value := range filter.values {
							if field.Equal(value) {
								shapes = append(shapes, record.Shape)
								continue Record
							}
						}
					}
				}
			}

			return scanner.Err()
		}(); err != nil {
			return err
		}
	}

	for _, filter := range filters {
		if _, ok := fields[filter.name]; !ok {
			return fmt.Errorf("unrecognized field '%s' not present in any shapefile", filter.name)
		}
	}

	if len(shapes) == 0 {
		return fmt.Errorf("no records selected")
	}

	f, err := os.Create(a.Destination)
	if err != nil {
		return fmt.Errorf("failed to create file '%s': %w", a.Destination, err)
	}

	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close file: %w", err)
		}
	}()

	box := shapes.BoundingBox()

	canvas := createCanvas(f, box, a.Scale)
	defer canvas.End()

	for _, shape := range shapes {
		switch v := shape.(type) {
		case shp.Point:
			renderPoint(canvas, v, box, a.Scale)
		case shp.Polyline:
			renderPolyline(canvas, v, box, a.Scale)
		case shp.Polygon:
			renderPolygon(canvas, v, box, a.Scale)
		}
	}
	return nil
}

type filter struct {
	name   string
	values []string
}

func (a App) parseFilters() ([]filter, error) {
	filters := make(map[string][]string)
	for _, str := range a.Filters {
		parts := strings.Split(str, "=")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid filter expression '%s'", str)
		}

		name := strings.TrimSpace(parts[0])
		valuesStr := strings.TrimSpace(parts[1])
		if name == "" || valuesStr == "" {
			return nil, fmt.Errorf("missing name or values from '%s'", str)
		}

		if valuesStr[0] == '[' && valuesStr[len(valuesStr)-1] == ']' {
			values := strings.Split(valuesStr[1:len(valuesStr)-1], ",")
			for i := 0; i < len(values); i++ {
				values[i] = strings.TrimSpace(values[i])
			}
			filters[name] = append(filters[name], values...)
		} else {
			filters[name] = append(filters[name], valuesStr)
		}
	}

	out := make([]filter, len(filters))
	var i int
	for name, values := range filters {
		out[i] = filter{name: name, values: values}
		i++
	}
	return out, nil
}

func open(path string) (shapefile.Scannable, io.Closer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open zip file '%s': %w", path, err)
	}

	close := func() error {
		if err := file.Close(); err != nil {
			return fmt.Errorf("failed to close zip file: %w", err)
		}
		return nil
	}

	stat, err := file.Stat()
	if err != nil {
		return nil, closer(close), fmt.Errorf("failed to stat zip file: %w", err)
	}

	_, name := filepath.Split(path)

	scanner, err := shapefile.NewZipScanner(file, stat.Size(), name)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize scanner: %w", err)
	}

	info, err := scanner.Info()
	if err != nil {
		return nil, nil, err
	}

	switch info.ShapeType {
	case
		shp.PointType,
		shp.PolylineType,
		shp.PolygonType:
		return scanner, closer(close), err
	default:
		return nil, nil, fmt.Errorf("unsupported shape type '%s'", info.ShapeType)
	}
}

type closer func() error

func (c closer) Close() error {
	return c()
}

func renderPoint(canvas *svg.SVG, point shp.Point, box shp.BoundingBox, scale float64) {
	x, y := mapPoint(point.X, point.Y, box, scale)
	canvas.Circle(x, y, int(math.Max(scale/10, 1)), `fill="red"`)
}

func renderPolyline(canvas *svg.SVG, polyline shp.Polyline, box shp.BoundingBox, scale float64) {
	for _, part := range polyline.Parts {
		var xs, ys []int
		for _, point := range part {
			x, y := mapPoint(point.X, point.Y, box, scale)
			xs = append(xs, x)
			ys = append(ys, y)
		}
		canvas.Polyline(xs, ys, lineStyle(scale)...)
	}
}

func renderPolygon(canvas *svg.SVG, polygon shp.Polygon, box shp.BoundingBox, scale float64) {
	for _, part := range polygon.Parts {
		var xs, ys []int
		for _, point := range part {
			x, y := mapPoint(point.X, point.Y, box, scale)
			xs = append(xs, x)
			ys = append(ys, y)
		}
		canvas.Polygon(xs, ys, lineStyle(scale)...)
	}
}

func lineStyle(scale float64) []string {
	return []string{
		`stroke="black"`,
		fmt.Sprintf(`stroke-width="%d"`, int(math.Max(scale/100, 1))),
		`fill="white"`,
		`fill-opacity="0"`,
	}
}

func mapPoint(x, y float64, box shp.BoundingBox, scale float64) (mappedX, mappedY int) {
	return int(math.Round((x - box.MinX) * scale)),
		int(math.Round(box.MaxY*scale)) - int(math.Round(box.MinY*scale)) - int(math.Round((y-box.MinY)*scale)) - 1
}

func canvasSize(box shp.BoundingBox, scale float64) (width, height int) {
	return int(math.Round(box.MaxX*scale)) - int(math.Round(box.MinX*scale)),
		int(math.Round(box.MaxY*scale)) - int(math.Round(box.MinY*scale))
}

func createCanvas(w io.Writer, box shp.BoundingBox, scale float64) *svg.SVG {
	out := svg.New(w)
	out.Start(canvasSize(box, scale))
	return out
}
