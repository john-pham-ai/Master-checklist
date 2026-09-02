package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

// defaultVehicleRange is the inclusive range of vehicle IDs offered in the
// Vehicle field's autofill list. Override with VEHICLE_RANGE, e.g. "801-840"
// or a mixed list "801-835,900,905-910".
const defaultVehicleRange = "801-835"

// parseVehicleRange expands "801-835,900" into ["801", ..., "835", "900"].
// Invalid segments are skipped with a log line rather than failing startup.
func parseVehicleRange(spec string) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, seg := range strings.Split(spec, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		lo, hi, ok := strings.Cut(seg, "-")
		if !ok {
			add(seg)
			continue
		}
		start, err1 := strconv.Atoi(strings.TrimSpace(lo))
		end, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || end < start || end-start > 10000 {
			log.Printf("vehicles: ignoring invalid range segment %q in VEHICLE_RANGE", seg)
			continue
		}
		for i := start; i <= end; i++ {
			add(fmt.Sprint(i))
		}
	}
	return ids
}
