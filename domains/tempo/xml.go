package tempo

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"time"
)

// The Tempo endpoint also publishes a legacy XML representation. It is not a
// transliteration of the JSON one: the elements are French, the colours are
// French, there is no time-of-day and no UTC offset, and the archive is not
// even self-consistent about the root element — some documents wrap the days in
// <Tempos> with an XML prolog, others are a bare <Tempo> with neither. It is
// decoded by scanning for <Tempo> elements, which covers every variant.

// parisZone is where Tempo days are anchored. The XML carries plain dates, so a
// location is needed to know when each day starts and ends.
const parisZone = "Europe/Paris"

// frenchColours maps the legacy colour names onto the JSON API's.
var frenchColours = map[string]Colour{
	"BLEU":  Blue,
	"BLANC": White,
	"ROUGE": Red,
}

// xmlTempo is one <Tempo> element.
type xmlTempo struct {
	// DateHeureCreation is when RTE published the colour, as a plain date.
	DateHeureCreation string `xml:"DateHeureCreation"`
	// DateApplication is the day the colour applies to.
	DateApplication string `xml:"DateApplication"`
	// Couleur is BLEU, BLANC or ROUGE.
	Couleur string `xml:"Couleur"`
}

// day converts the element into a domain Day anchored in loc.
func (x xmlTempo) day(loc *time.Location) (Day, error) {
	colour, ok := frenchColours[x.Couleur]
	if !ok {
		return Day{}, fmt.Errorf("tempo: unknown colour %q in xml response", x.Couleur)
	}

	start, err := parseLocalDate(x.DateApplication, loc)
	if err != nil {
		return Day{}, fmt.Errorf("tempo: DateApplication: %w", err)
	}
	updated, err := parseLocalDate(x.DateHeureCreation, loc)
	if err != nil {
		return Day{}, fmt.Errorf("tempo: DateHeureCreation: %w", err)
	}

	return Day{
		Start: start,
		// A Tempo day runs to the next local midnight, which is 23 or 25 hours
		// away on the days France changes clocks. AddDate keeps that boundary
		// right where adding 24h would drift it.
		End:       start.AddDate(0, 0, 1),
		Colour:    colour,
		UpdatedAt: updated,
	}, nil
}

func parseLocalDate(value string, loc *time.Location) (time.Time, error) {
	parsed, err := time.ParseInLocation(time.DateOnly, value, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %q: %w", value, err)
	}
	return parsed, nil
}

// decodeXMLCalendars decodes the legacy XML representation.
func decodeXMLCalendars(body []byte) (Calendars, error) {
	paris, err := time.LoadLocation(parisZone)
	if err != nil {
		return nil, fmt.Errorf(
			`tempo: load %s (import _ "time/tzdata" to embed the zone database): %w`, parisZone, err)
	}

	decoder := xml.NewDecoder(bytes.NewReader(body))
	days := []Day{}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tempo: decode xml response: %w", err)
		}

		// Scanning for <Tempo> rather than unmarshalling a fixed root is what
		// makes the wrapped and the bare document both work.
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "Tempo" {
			continue
		}

		var entry xmlTempo
		if err := decoder.DecodeElement(&entry, &element); err != nil {
			return nil, fmt.Errorf("tempo: decode Tempo element: %w", err)
		}
		day, err := entry.day(paris)
		if err != nil {
			return nil, err
		}
		days = append(days, day)
	}

	if len(days) == 0 {
		return Calendars{}, nil
	}
	return Calendars{{Start: earliestStart(days), End: latestEnd(days), Days: days}}, nil
}

func earliestStart(days []Day) time.Time {
	earliest := days[0].Start
	for _, d := range days[1:] {
		if d.Start.Before(earliest) {
			earliest = d.Start
		}
	}
	return earliest
}

func latestEnd(days []Day) time.Time {
	latest := days[0].End
	for _, d := range days[1:] {
		if d.End.After(latest) {
			latest = d.End
		}
	}
	return latest
}
