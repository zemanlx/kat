package conformance

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"k8s.io/apimachinery/pkg/util/version"
	"sigs.k8s.io/yaml"
)

var errBadDivergence = errors.New("invalid known divergence")

// divergence is one entry of known-divergences.yaml: a case, or a whole suite,
// expected to differ from the kube-apiserver.
type divergence struct {
	Suite string `json:"suite"`
	// Case is the test name without ".yaml"; empty means every case.
	Case string `json:"case,omitempty"`
	// MinVersion and MaxVersion bound the Kubernetes minor versions the entry
	// applies to, inclusive; empty means unbounded.
	MinVersion string `json:"minVersion,omitempty"`
	MaxVersion string `json:"maxVersion,omitempty"`
	Reason     string `json:"reason"`
	Issue      string `json:"issue,omitempty"`

	minVersion, maxVersion *version.Version
}

type divergences struct {
	entries []*divergence

	mu      sync.Mutex
	matched map[*divergence]bool
	ran     map[string]bool
}

func loadDivergences(path string) (*divergences, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var file struct {
		Divergences []*divergence `json:"divergences"`
	}

	if err := yaml.UnmarshalStrict(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	for _, d := range file.Divergences {
		if d.Suite == "" || d.Reason == "" {
			return nil, fmt.Errorf("%w: %+v: suite and reason are required", errBadDivergence, d)
		}

		for _, bound := range []struct {
			text string
			dst  **version.Version
		}{{d.MinVersion, &d.minVersion}, {d.MaxVersion, &d.maxVersion}} {
			if bound.text == "" {
				continue
			}

			v, err := version.ParseGeneric(bound.text)
			if err != nil {
				return nil, fmt.Errorf("%w: %s: %w", errBadDivergence, d.Suite, err)
			}

			*bound.dst = v
		}
	}

	return &divergences{entries: file.Divergences, matched: map[*divergence]bool{}, ran: map[string]bool{}}, nil
}

// lookup returns the entry covering the case on the given server version.
func (d *divergences) lookup(suiteID, name string, ver *version.Version) *divergence {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.ran[suiteID] = true

	for _, e := range d.entries {
		if e.Suite != suiteID || (e.Case != "" && e.Case != name) {
			continue
		}

		minor := version.MajorMinor(ver.Major(), ver.Minor())
		if (e.minVersion != nil && minor.LessThan(version.MajorMinor(e.minVersion.Major(), e.minVersion.Minor()))) ||
			(e.maxVersion != nil && version.MajorMinor(e.maxVersion.Major(), e.maxVersion.Minor()).LessThan(minor)) {
			continue
		}

		d.matched[e] = true

		return e
	}

	return nil
}

// unmatched returns the entries of suites that ran but that matched no case,
// such as an entry whose case was renamed.
func (d *divergences) unmatched(ver *version.Version) []*divergence {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*divergence

	for _, e := range d.entries {
		if !d.ran[e.Suite] || d.matched[e] {
			continue
		}

		minor := version.MajorMinor(ver.Major(), ver.Minor())
		inRange := (e.minVersion == nil || !minor.LessThan(version.MajorMinor(e.minVersion.Major(), e.minVersion.Minor()))) &&
			(e.maxVersion == nil || !version.MajorMinor(e.maxVersion.Major(), e.maxVersion.Minor()).LessThan(minor))

		if inRange {
			out = append(out, e)
		}
	}

	return out
}
