package app

import "context"

// InspectLocal and ExportLocal are the shared media operations used by the
// embeddable engine. They do not need a database or a server.
func InspectLocal(ctx context.Context, c Config, path string) (Source, error) {
	p, duration, err := probe(ctx, c, path, false)
	if err != nil {
		return Source{}, &localInspectionFailure{cause: err}
	}
	s := Source{DurationMS: duration, Path: path, Kind: "upload"}
	for _, v := range p.Streams {
		if v.CodecType == "video" {
			s.Width = v.Width
			s.Height = v.Height
			break
		}
	}
	return s, nil
}

func ExportLocal(ctx context.Context, c Config, path, out string, r Range, options ExportRequest) (int64, int64, error) {
	return exportMedia(ctx, c, []string{path}, false, r, options, out)
}
