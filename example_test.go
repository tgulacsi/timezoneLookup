// Copyright 2018 Evan Oberholster.
//
// SPDX-License-Identifier: MIT

package timezoneLookup_test

import (
	"fmt"

	timezone "github.com/evanoberholster/timezoneLookup"
)

func ExampleQuery() {
	tz, err := timezone.LoadTimezones(timezone.Config{
		DatabaseType: "boltdb",   // memory or boltdb
		DatabaseName: "timezone", // Name without suffix
		Snappy:       false,
		Encoding:     timezone.EncCapnProto, // json or capnp
	})
	if err != nil {
		fmt.Println(err)
	}
	defer tz.Close()

	res, err := tz.Query(timezone.Coord{
		Lat: 5.261417, Lon: -3.925778})
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("Query Result: ", res)
}
