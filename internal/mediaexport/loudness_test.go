package mediaexport

import (
	"encoding/json"
	"math"
	"testing"
)

func TestLoudnessReportParsesCompleteBoundedMachineBlock(t *testing.T) {
	values := map[string]string{"input_i": "-20.1", "input_tp": "-10.0", "input_lra": "2.3", "input_thresh": "-30.5", "output_i": "-16.0", "output_tp": "-5.9", "output_lra": "2.3", "output_thresh": "-26.4", "normalization_type": "dynamic", "target_offset": "0.01"}
	block, _ := json.MarshalIndent(values, "", "\t")
	data := append([]byte("private file: /not-for-errors\n[Parsed_loudnorm_0 @ address]\n"), block...)
	data = append(data, []byte("\n[out#0] video:0KiB\n")...)
	r, err := ParseLoudnessReport(data)
	if err != nil || r.IntegratedLUFS == nil || math.Abs(*r.IntegratedLUFS+20.1) > .001 {
		t.Fatal(r, err)
	}
	for _, key := range []string{"input_i", "input_tp", "input_lra", "input_thresh", "target_offset"} {
		old := values[key]
		values[key] = "NaN"
		raw, _ := json.MarshalIndent(values, "", "\t")
		if _, err := ParseLoudnessReport(raw); err == nil {
			t.Fatal("nonfinite field accepted", key)
		}
		values[key] = old
	}
	delete(values, "target_offset")
	block, _ = json.MarshalIndent(values, "", "\t")
	if _, err := ParseLoudnessReport(block); err == nil {
		t.Fatal("incomplete report accepted")
	}
}
