package tempo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// oneOrMany decodes a JSON value that may be either a single object or an array
// of objects into a slice. RTE uses both shapes for the same field: the live API
// returns "tempo_like_calendars" as an object where Swagger.json declares an
// array, and the 2016 samples return "values" as an object where both the spec
// and the live API return an array.
func oneOrMany[T any](raw json.RawMessage, out *[]T) error {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	switch {
	case len(trimmed) == 0, bytes.Equal(trimmed, []byte("null")):
		return nil
	case trimmed[0] == '[':
		return json.Unmarshal(raw, out)
	default:
		var single T
		if err := json.Unmarshal(raw, &single); err != nil {
			return err
		}
		*out = []T{single}
		return nil
	}
}

// flexBool is a boolean that may arrive as a JSON bool or as a quoted string.
// Swagger.json types "fallback" as a string enum of "true"/"false"; the sample
// archive emits a real bool.
type flexBool bool

func (f *flexBool) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if quoted := len(trimmed) > 1 && trimmed[0] == '"'; quoted {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*f = flexBool(strings.EqualFold(s, "true"))
		return nil
	}
	var b bool
	if err := json.Unmarshal(trimmed, &b); err != nil {
		return err
	}
	*f = flexBool(b)
	return nil
}

type wireResponse struct {
	Calendars json.RawMessage `json:"tempo_like_calendars"`
}

type wireCalendar struct {
	StartDate time.Time       `json:"start_date"`
	EndDate   time.Time       `json:"end_date"`
	Values    json.RawMessage `json:"values"`
}

type wireDay struct {
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	// The colour is keyed "value" by the live API and the sample archive, but
	// "values" by Swagger.json. Both are read and whichever is set wins.
	Value       Colour    `json:"value"`
	Values      Colour    `json:"values"`
	Fallback    flexBool  `json:"fallback"`
	UpdatedDate time.Time `json:"updated_date"`
}

func (w wireDay) colour() Colour {
	if w.Value != "" {
		return w.Value
	}
	return w.Values
}

func (w wireDay) day() Day {
	return Day{
		Start:     w.StartDate,
		End:       w.EndDate,
		Colour:    w.colour(),
		Fallback:  bool(w.Fallback),
		UpdatedAt: w.UpdatedDate,
	}
}

func decodeJSONCalendars(body []byte) (Calendars, error) {
	var response wireResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("tempo: decode response: %w", err)
	}

	var wireCalendars []wireCalendar
	if err := oneOrMany(response.Calendars, &wireCalendars); err != nil {
		return nil, fmt.Errorf("tempo: decode tempo_like_calendars: %w", err)
	}

	calendars := make(Calendars, 0, len(wireCalendars))
	for _, wc := range wireCalendars {
		var wireDays []wireDay
		if err := oneOrMany(wc.Values, &wireDays); err != nil {
			return nil, fmt.Errorf("tempo: decode values: %w", err)
		}
		days := make([]Day, 0, len(wireDays))
		for _, wd := range wireDays {
			days = append(days, wd.day())
		}
		calendars = append(calendars, Calendar{Start: wc.StartDate, End: wc.EndDate, Days: days})
	}
	return calendars, nil
}
