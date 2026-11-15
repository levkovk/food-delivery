package main

import "time"

// Число неудачных попыток назначения, после которого курьер ищется
// в соседних зонах.
const fallbackAfter = 5

// candidateZones возвращает зоны, в которых ищется курьер для заказа.
// Сначала только своя зона, затем после нескольких попыток - любая.
func candidateZones(zone string, attempts int) []string {
	if attempts < fallbackAfter {
		return []string{zone}
	}
	out := []string{zone}
	for _, z := range []string{"center", "north", "south"} {
		if z != zone {
			out = append(out, z)
		}
	}
	return out
}

// travelTime - модель времени в пути: зависит от транспорта и от того,
// приходится ли курьеру ехать из другой зоны. minute - длительность минуты модели.
func travelTime(transport string, sameZone bool, minute time.Duration) time.Duration {
	base := map[string]int{"пешком": 25, "самокат": 18, "велосипед": 15, "автомобиль": 12}[transport]
	if base == 0 {
		base = 20
	}
	if !sameZone {
		base += 10
	}
	return time.Duration(base) * minute
}
