// Copyright 2018 Evan Oberholster.
//
// SPDX-License-Identifier: MIT

package timezoneLookup

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/evanoberholster/timezoneLookup/fb"
	"github.com/evanoberholster/timezoneLookup/pb"
	json "github.com/goccy/go-json"
	flatbuffers "github.com/google/flatbuffers/go"
)

//go:generate go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
//go:generate protoc --proto_path=pb --go_out=pb --go_opt=paths=source_relative pb/timezone.proto

//go:generate flatc --go --gen-onefile -o fb/ --go-namespace fb fb/timezone.fbs

const (
	WithSnappy = true
	NoSnappy   = false

	// Errors
	errNotExistGeoJSON    = "Error: GeoJSON file does not exist"
	errExistDatabase      = "Error: Destination Database file already exists"
	errNotExistDatabase   = "Error: Database file does not exist"
	errPolygonNotFound    = "Error: Polygon for Timezone not found"
	errTimezoneNotFound   = "Error: Timezone not found"
	errDatabaseTypeUknown = "Error: Database type unknown"
)

type TimezoneInterface interface {
	CreateTimezones(jsonFilename string) error
	LoadTimezones() error
	Query(q Coord) (string, error)
	Close()
}

type TimezoneGeoJSON struct {
	Type     string `json:"type"`
	Features []struct {
		Type       string `json:"type"`
		Properties struct {
			Tzid string `json:"tzid"`
		} `json:"properties"`
		Geometry struct {
			Item        string        `json:"type"`
			Coordinates []interface{} `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

type Timezone struct {
	Tzid     string    `json:"tzid"`
	Polygons []Polygon `json:"polygons"`
}

type Polygon struct {
	Max    Coord   `json:"max"`
	Min    Coord   `json:"min"`
	Coords []Coord `json:"coords"`
}

func (dst *Polygon) FromFB(src *fb.Polygon) {
	var c fb.Coord
	dst.Max.FromFB(src.Max(&c))
	dst.Min.FromFB(src.Min(&c))
	length := src.CoordsLength()
	if cap(dst.Coords) < length {
		dst.Coords = make([]Coord, length)
	} else {
		dst.Coords = dst.Coords[:length]
	}
	for i := 0; i < length; i++ {
		src.Coords(&c, i)
		dst.Coords[i].FromFB(&c)
	}
}
func (src Polygon) ToFB(dst *fb.Polygon) {
	b := flatbuffers.NewBuilder(512)
	fb.PolygonStart(b)
	fb.PolygonAddMax(b, fb.CreateCoord(b, src.Max.Lat, src.Max.Lon))
	fb.PolygonAddMin(b, fb.CreateCoord(b, src.Min.Lat, src.Min.Lon))
	fb.PolygonStartCoordsVector(b, len(src.Coords))
	for _, c := range src.Coords {
		fb.CreateCoord(b, c.Lat, c.Lon)
	}
	off := fb.PolygonEnd(b)
	b.Finish(off)
	dst.Init(b.FinishedBytes(), off)
}

func (dst *Polygon) FromPB(src *pb.Polygon) {
	dst.Max.FromPB(src.Max)
	dst.Min.FromPB(src.Min)
	if cap(dst.Coords) < len(src.Coords) {
		dst.Coords = make([]Coord, len(src.Coords))
	} else {
		dst.Coords = dst.Coords[:len(src.Coords)]
	}
	for i, c := range src.Coords {
		dst.Coords[i].FromPB(c)
	}
}
func (src Polygon) ToPB(dst *pb.Polygon) {
	dst.Reset()
	dst.Max = src.Max.ToPB(dst.Max)
	dst.Min = src.Min.ToPB(dst.Min)
	if cap(dst.Coords) < len(src.Coords) {
		dst.Coords = make([]*pb.Coord, len(src.Coords))
	} else {
		dst.Coords = dst.Coords[:len(src.Coords)]
	}
	for i, c := range src.Coords {
		dst.Coords[i] = c.ToPB(dst.Coords[i])
	}
}

type Coord struct {
	Lat float32 `json:"lat"`
	Lon float32 `json:"lon"`
}

func (src Coord) ToPB(dst *pb.Coord) *pb.Coord {
	if dst == nil {
		return &pb.Coord{Lat: src.Lat, Lon: src.Lon}
	}
	dst.Reset()
	dst.Lat, dst.Lon = src.Lat, src.Lon
	return dst
}
func (dst *Coord) FromPB(src *pb.Coord) {
	dst.Lat, dst.Lon = src.Lat, src.Lon
}

func (src Coord) ToFB(dst *fb.Coord) *fb.Coord {
	b := flatbuffers.NewBuilder(16)
	off := fb.CreateCoord(b, src.Lat, src.Lon)
	b.Finish(off)
	dst.Init(b.FinishedBytes(), off)
	return dst
}
func (dst *Coord) FromFB(src *fb.Coord) {
	dst.Lat, dst.Lon = src.Lat(), src.Lon()
}

type Config struct {
	DatabaseName string
	DatabaseType string
	Snappy       bool
	Encoding     encoding
}

var Tz TimezoneInterface

func LoadTimezones(config Config) (TimezoneInterface, error) {
	if config.DatabaseType == "memory" {
		tz := MemoryStorage(config.Snappy, config.DatabaseName)
		err := tz.LoadTimezones()
		return tz, err

	} else if config.DatabaseType == "boltdb" {
		tz := BoltdbStorage(config.Snappy, config.DatabaseName, config.Encoding)
		err := tz.LoadTimezones()
		return tz, err
	}
	return &Memory{}, errors.New(errDatabaseTypeUknown)
}

func TimezonesFromGeoJSON(filename string) ([]Timezone, error) {
	start_decode := time.Now()
	fmt.Println("Building Timezone Database from: ", filename)
	var timeZones []Timezone
	file, err := os.Open(filename)
	if err != nil {
		return timeZones, err
	}
	dec := json.NewDecoder(file)

	for dec.More() {
		var js TimezoneGeoJSON

		err := dec.Decode(&js)
		if err != nil {
			return timeZones, err
		}
		for _, tz := range js.Features {
			t := Timezone{Tzid: tz.Properties.Tzid}
			switch tz.Geometry.Item {
			case "Polygon":
				t.decodePolygons(tz.Geometry.Coordinates)
			case "MultiPolygon":
				t.decodeMultiPolygons(tz.Geometry.Coordinates)
			}
			timeZones = append(timeZones, t)
		}
	}
	elapsed_decode := time.Since(start_decode)
	fmt.Println("GeoJSON decode took: ", elapsed_decode, " with ", len(timeZones), " Timezones loaded from GeoJSON")
	return timeZones, nil
}

func (t *Timezone) decodePolygons(polys []interface{}) { //1
	for _, points := range polys {
		p := t.newPolygon()
		for _, point := range points.([]interface{}) { //3
			p.updatePolygon(point.([]interface{}))
		}
		t.Polygons = append(t.Polygons, p)
	}
}

func (t *Timezone) decodeMultiPolygons(polys []interface{}) { //1
	for _, v := range polys {
		p := t.newPolygon()
		for _, points := range v.([]interface{}) { // 2
			for _, point := range points.([]interface{}) { //3
				p.updatePolygon(point.([]interface{}))
			}
		}
		t.Polygons = append(t.Polygons, p)
	}
}

func (t *Timezone) newPolygon() Polygon {
	return Polygon{
		Max: Coord{Lat: -90, Lon: -180},
		Min: Coord{Lat: 90, Lon: 180},
	}
}

func (p *Polygon) updatePolygon(xy []interface{}) {
	lon := float32(xy[0].(float64))
	lat := float32(xy[1].(float64))

	// Update max and min limits
	if p.Max.Lat < lat {
		p.Max.Lat = lat
	}
	if p.Max.Lon < lon {
		p.Max.Lon = lon
	}
	if p.Min.Lat > lat {
		p.Min.Lat = lat
	}
	if p.Min.Lon > lon {
		p.Min.Lon = lon
	}

	// add Coords to Polygon
	p.Coords = append(p.Coords, Coord{Lat: lat, Lon: lon})
}

func (p *Polygon) contains(queryPt Coord) bool {
	if len(p.Coords) < 3 {
		return false
	}
	in := rayIntersectsSegment(queryPt, p.Coords[len(p.Coords)-1], p.Coords[0])
	for i := 1; i < len(p.Coords); i++ {
		if rayIntersectsSegment(queryPt, p.Coords[i-1], p.Coords[i]) {
			in = !in
		}
	}
	return in
}

func rayIntersectsSegment(p, a, b Coord) bool {
	return (a.Lon > p.Lon) != (b.Lon > p.Lon) &&
		p.Lat < (b.Lat-a.Lat)*(p.Lon-a.Lon)/(b.Lon-a.Lon)+a.Lat
}

func PrintMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// For info on each, see: https://golang.org/pkg/runtime/#MemStats
	fmt.Printf("Allocated Memory = %v MiB", bToMb(m.Alloc))
	fmt.Printf("\tTotal Allocated Memory = %v MiB", bToMb(m.TotalAlloc))
	fmt.Printf("\tSystem Memory = %v MiB", bToMb(m.Sys))
	fmt.Printf("\tNumber of GC = %v\n", m.NumGC)
}

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}
