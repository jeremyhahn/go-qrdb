package hnsw

import (
	"errors"
	"strings"
	"testing"
)

func TestParseDistanceMetric_ResolvesEveryNameInEitherCase(t *testing.T) {
	tests := []struct {
		name string
		want DistanceMetric
	}{
		{"", L2Distance},
		{"L2", L2Distance},
		{"l2", L2Distance},
		{"EUCLIDEAN", L2Distance},
		{"euclidean", L2Distance},
		{"COSINE", CosineDistance},
		{"Cosine", CosineDistance},
		{"DOT_PRODUCT", DotProductDistance},
		{"dot_product", DotProductDistance},
		{"MANHATTAN", ManhattanDistance},
		{"manhattan", ManhattanDistance},
	}
	for _, tt := range tests {
		got, err := ParseDistanceMetric(tt.name)
		if err != nil {
			t.Fatalf("ParseDistanceMetric(%q) error = %v", tt.name, err)
		}
		if got != tt.want {
			t.Errorf("ParseDistanceMetric(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestParseDistanceMetric_RefusesAnUnknownName(t *testing.T) {
	for _, name := range []string{"HAMMING", "unknown", "l1", " L2"} {
		_, err := ParseDistanceMetric(name)
		var unknown *UnknownDistanceMetricError
		if !errors.As(err, &unknown) {
			t.Fatalf("ParseDistanceMetric(%q) error = %T (%v), want *UnknownDistanceMetricError", name, err, err)
		}
		if unknown.Name != name {
			t.Errorf("UnknownDistanceMetricError.Name = %q, want %q", unknown.Name, name)
		}
	}
}

func TestDistanceMetricNames_AreUpperCase(t *testing.T) {
	for name := range distanceMetricNames {
		if name != strings.ToUpper(name) {
			t.Errorf("distanceMetricNames key %q is not upper case; ParseDistanceMetric could never find it", name)
		}
	}
}

func TestUnknownDistanceMetricError_NamesTheMetricAndTheChoices(t *testing.T) {
	msg := (&UnknownDistanceMetricError{Name: "HAMMING"}).Error()
	for _, want := range []string{`"HAMMING"`, "L2", "EUCLIDEAN", "COSINE", "DOT_PRODUCT", "MANHATTAN"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestUnknownDistanceMetricError_QuotesControlCharacters(t *testing.T) {
	msg := (&UnknownDistanceMetricError{Name: "a\nb"}).Error()
	if strings.Contains(msg, "\n") {
		t.Errorf("Error() = %q carries a raw newline", msg)
	}
}
