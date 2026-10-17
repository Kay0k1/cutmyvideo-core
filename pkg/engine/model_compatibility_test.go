package engine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

var (
	_ *edit.Crop                                            = (*Crop)(nil)
	_ *edit.Track                                           = (*Track)(nil)
	_ *edit.TrackSelection                                  = (*TrackSelection)(nil)
	_ *edit.Segment                                         = (*Segment)(nil)
	_ *edit.CollectionPlan                                  = (*CollectionPlan)(nil)
	_ *edit.CollectionClip                                  = (*CollectionClip)(nil)
	_ *edit.CollectionOptions                               = (*CollectionOptions)(nil)
	_ *edit.CollectionExecution                             = (*CollectionExecution)(nil)
	_ *edit.CollectionFieldError                            = (*CollectionFieldError)(nil)
	_ func(*PreflightRequest, app.Config, app.Source) error = (*PreflightRequest).Validate
)

func TestEngineEditModelPreservesPublicShapes(t *testing.T) {
	// These established positional forms must remain source-compatible. Range
	// intentionally has two fields; CollectionRange also carries a label.
	rangeValue := Range{0, 1000}
	_ = Options{"mp4", "best", "accurate"}
	collectionRange := CollectionRange{StartMS: 0, EndMS: 1000, Label: "label"}
	crop := Crop{Width: 1, Height: 1}
	shape := reflect.TypeOf(rangeValue)
	if shape.NumField() != 2 || shape.Field(0).Name != "StartMS" || shape.Field(1).Name != "EndMS" {
		t.Fatal("public Range changed its two-field contract")
	}
	for _, test := range []struct {
		value any
		json  string
	}{
		{rangeValue, `{"StartMS":0,"EndMS":1000}`},
		{collectionRange, `{"start_ms":0,"end_ms":1000,"label":"label"}`},
		{crop, `{"x":0,"y":0,"width":1,"height":1}`},
	} {
		encoded, err := json.Marshal(test.value)
		if err != nil || string(encoded) != test.json {
			t.Fatal("public model JSON changed", string(encoded), test.json, err)
		}
	}
}
