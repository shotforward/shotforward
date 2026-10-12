package projecthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestEpisodeReferenceMediaTrimPatchContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, fields string
		status       int
		present      bool
		start, end   int64
	}{
		{name: "omitted preserves", fields: ``, status: http.StatusOK},
		{name: "selection", fields: `,"trim_start_ms":3000,"trim_end_ms":11000`, status: http.StatusOK, present: true, start: 3000, end: 11000},
		{name: "reset", fields: `,"trim_start_ms":0,"trim_end_ms":0`, status: http.StatusOK, present: true},
		{name: "missing end", fields: `,"trim_start_ms":3000`, status: http.StatusBadRequest},
		{name: "missing start", fields: `,"trim_end_ms":11000`, status: http.StatusBadRequest},
		{name: "null pair", fields: `,"trim_start_ms":null,"trim_end_ms":null`, status: http.StatusBadRequest},
		{name: "null start", fields: `,"trim_start_ms":null,"trim_end_ms":11000`, status: http.StatusBadRequest},
		{name: "null end", fields: `,"trim_start_ms":0,"trim_end_ms":null`, status: http.StatusBadRequest},
		{name: "fraction", fields: `,"trim_start_ms":0.5,"trim_end_ms":11000`, status: http.StatusBadRequest},
		{name: "string", fields: `,"trim_start_ms":"0","trim_end_ms":11000`, status: http.StatusBadRequest},
		{name: "boolean", fields: `,"trim_start_ms":false,"trim_end_ms":11000`, status: http.StatusBadRequest},
		{name: "unknown", fields: `,"trim_start_ms":0,"trim_end_ms":11000,"clip_id":"other"`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, projectID, episodeID, _, _ := assetHTTPFixture()
			artifactID := project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
			service.episodeReferenceMedia = []project.EpisodeReferenceMedia{{EpisodeID: episodeID, Artifact: project.Artifact{ID: artifactID, ProjectID: projectID, Kind: project.MediaKindAudio, DurationMilliseconds: 15000}, Name: "音频", Revision: 1}}
			handler, err := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/projects/" + projectID.String() + "/episodes/" + episodeID.String() + "/reference-media/" + artifactID.String()
			request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"name":"音频","description":"保留","expected_revision":1`+test.fields+`}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			input := service.episodeReferenceUpdateInput
			if test.status != http.StatusOK {
				if input.ArtifactID != "" {
					t.Fatal("invalid bounds reached service")
				}
				return
			}
			if (input.TrimStartMilliseconds != nil) != test.present || (input.TrimEndMilliseconds != nil) != test.present {
				t.Fatalf("bounds presence lost: %#v", input)
			}
			if test.present && (*input.TrimStartMilliseconds != test.start || *input.TrimEndMilliseconds != test.end) {
				t.Fatalf("bounds changed: %#v", input)
			}
		})
	}
}

func TestMapEpisodeReferenceMediaKeepsOriginalDurationAndExposesTrim(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ start, end, effective int64 }{{0, 0, 15000}, {3000, 11000, 8000}} {
		value := project.EpisodeReferenceMedia{Artifact: project.Artifact{Kind: project.MediaKindAudio, DurationMilliseconds: 15000}, TrimStartMilliseconds: test.start, TrimEndMilliseconds: test.end}
		mapped := mapEpisodeReferenceMedia(value)
		if mapped["trim_start_ms"] != test.start || mapped["trim_end_ms"] != test.end || mapped["effective_duration_ms"] != test.effective {
			t.Fatalf("mapped trim = %#v", mapped)
		}
		encoded, err := json.Marshal(mapped)
		if err != nil || !strings.Contains(string(encoded), `"duration_ms":15000`) {
			t.Fatalf("original duration not preserved: %s, %v", encoded, err)
		}
	}
}
