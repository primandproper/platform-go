package links

import (
	"encoding/json"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// Duration is a lifetime that survives the trip through a configuration file.
//
// It exists because JSON has no duration. A time.Duration is an int64 to
// encoding/json, so the only spelling a JSON file has for fifteen minutes is
// 900000000000 — and the action registry is deliberately a file somebody
// reviews (see the Actions field of links/config.Config), which is a review
// nobody can perform against a nanosecond count. A Duration reads and writes
// "15m" instead, in everything time.ParseDuration accepts: "1h30m", "168h",
// "30s".
//
// YAML already understood "15m" against a bare time.Duration, because
// gopkg.in/yaml.v3 special-cases that one type. A named type is not that type,
// so the YAML methods here are what keep the promise the yaml struct tag was
// already making rather than a new one this type adds.
//
// A bare number is refused rather than read as nanoseconds. "ttl": 15 from
// somebody who meant fifteen minutes would otherwise pass every check
// downstream — the lifetime is positive, the URL is fine — and mint links that
// are dead fifteen nanoseconds after they are written into an email. That is
// the one misconfiguration in this field that nothing else can catch, so it is
// caught at the only moment it is visible, which is while the file is being
// read.
type Duration time.Duration

var (
	_ json.Marshaler   = Duration(0)
	_ json.Unmarshaler = (*Duration)(nil)
)

// String renders the duration the way a configuration file spells it.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// MarshalJSON writes the duration as a string, so that a file this package
// wrote is a file this package documents.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON reads a duration written as a string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return platformerrors.Wrapf(ErrInvalidTTL, "duration must be a quoted string such as \"15m\", got %s", data)
	}

	return d.parse(text)
}

// MarshalYAML writes the duration as a string.
func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

// UnmarshalYAML reads a duration written as a string.
//
// The signature is the one taking a decode function rather than
// gopkg.in/yaml.v3's *yaml.Node. Every YAML library in use understands it —
// v3 still honors it, v2 and goccy/go-yaml know only it — and it costs this
// package no import of a YAML library at all, which a domain type that is
// merely decoded from one should not need.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var text string
	if err := unmarshal(&text); err != nil {
		return platformerrors.Wrapf(ErrInvalidTTL, `duration must be a string such as "15m": %s`, err.Error())
	}

	return d.parse(text)
}

// parse is the one place a written duration becomes this one, so the two
// encodings cannot come to disagree about what "15m" means or about what is
// said when it is misspelled.
func (d *Duration) parse(text string) error {
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return platformerrors.Wrapf(ErrInvalidTTL, "parsing duration %q", text)
	}

	*d = Duration(parsed)

	return nil
}
